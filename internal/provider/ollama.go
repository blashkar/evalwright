package provider

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/blashkar/evalwright/internal/ollama"
)

// Ollama runs models on an Ollama server.
type Ollama struct {
	Client *ollama.Client
	// AutoPull downloads models that aren't installed instead of failing.
	AutoPull bool
	// Log receives progress messages such as download status. May be nil.
	Log io.Writer
}

var (
	_ Provider = (*Ollama)(nil)
	_ Preparer = (*Ollama)(nil)
	_ Warmer   = (*Ollama)(nil)
)

// Generate implements Provider. The response is streamed, so cancelling ctx
// stops generation immediately instead of waiting for the model to finish.
func (o *Ollama) Generate(ctx context.Context, req Request) (Response, error) {
	msgs := make([]ollama.Message, 0, 2)
	if req.System != "" {
		msgs = append(msgs, ollama.Message{Role: "system", Content: req.System})
	}
	msgs = append(msgs, ollama.Message{Role: "user", Content: req.Prompt})

	start := time.Now()
	var text strings.Builder
	var m Metrics
	err := o.Client.Chat(ctx, ollama.ChatRequest{Model: req.Model, Messages: msgs, Options: req.Options},
		func(r ollama.ChatResponse) error {
			// A thinking model's first tokens are its reasoning, which also
			// keeps the user waiting, so either kind counts as the first token.
			if m.TimeToFirstToken == 0 && (r.Message.Content != "" || r.Message.Thinking != "") {
				m.TimeToFirstToken = time.Since(start)
			}
			text.WriteString(r.Message.Content)
			if r.Done {
				m.PromptTokens, m.PromptDuration = r.PromptEvalCount, r.PromptEvalDuration
				m.OutputTokens, m.GenerationDuration = r.EvalCount, r.EvalDuration
			}
			return nil
		})
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text.String(), Latency: time.Since(start), Metrics: m}, nil
}

// Prepare implements Preparer. It checks that model is installed and, if
// AutoPull is set, downloads it when it isn't. It returns the model's
// digest, size and quantization, and the server's version.
func (o *Ollama) Prepare(ctx context.Context, model string) (ModelInfo, error) {
	show, err := o.Client.Show(ctx, model)
	if err != nil {
		switch {
		case !ollama.IsNotFound(err):
			return ModelInfo{}, err
		case !o.AutoPull:
			return ModelInfo{}, fmt.Errorf("model %q is not installed; run `ollama pull %s` or pass --pull", model, model)
		}
		if err := o.pull(ctx, model); err != nil {
			return ModelInfo{}, fmt.Errorf("pulling %s: %w", model, err)
		}
		if show, err = o.Client.Show(ctx, model); err != nil {
			return ModelInfo{}, err
		}
	}
	info := ModelInfo{
		Family:        show.Details.Family,
		ParameterSize: show.Details.ParameterSize,
		Quantization:  show.Details.QuantizationLevel,
		Defaults:      parseParameters(show.Parameters),
	}

	// /api/show doesn't report the digest; the list of installed models does.
	models, err := o.Client.List(ctx)
	if err != nil {
		return ModelInfo{}, err
	}
	for _, m := range models {
		if sameModel(m.Name, model) {
			info.Digest = m.Digest
			break
		}
	}

	version, err := o.Client.Version(ctx)
	if err != nil {
		return ModelInfo{}, err
	}
	info.Engine = "ollama " + version
	return info, nil
}

// parseParameters reads a model's default options from /api/show, one
// "name value" per line. Numbers are returned as float64 and other values
// as strings. stop, which lists the tokens that end an answer and can
// repeat, isn't an option evalwright reports, so it is skipped.
func parseParameters(text string) map[string]any {
	out := map[string]any{}
	for line := range strings.Lines(text) {
		name, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		value = strings.TrimSpace(value)
		if !ok || name == "stop" || value == "" {
			continue
		}
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			out[name] = f
		} else if s, err := strconv.Unquote(value); err == nil {
			out[name] = s
		} else {
			out[name] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameModel reports whether name, as the server lists it, is model. Like
// Ollama, a model named without a tag means its :latest tag.
func sameModel(name, model string) bool {
	return name == model || name == model+":latest"
}

func (o *Ollama) pull(ctx context.Context, model string) error {
	o.logf("Pulling %s...\n", model)
	var digest string
	lastPct := -1
	return o.Client.Pull(ctx, model, func(p ollama.PullProgress) error {
		if p.Total == 0 {
			return nil
		}
		if p.Digest != digest { // a new layer starts at 0%
			digest, lastPct = p.Digest, -1
		}
		// Report in 10% steps so the log stays readable.
		if pct := int(p.Completed*100/p.Total) / 10 * 10; pct != lastPct {
			lastPct = pct
			o.logf("  %3d%% of %s\n", pct, formatBytes(p.Total))
		}
		return nil
	})
}

// Warm implements Warmer. It loads model, then asks the server where it
// put it.
func (o *Ollama) Warm(ctx context.Context, model string, options map[string]any) (LoadInfo, error) {
	if err := o.Client.Load(ctx, model, options); err != nil {
		return LoadInfo{}, err
	}
	// The model is loaded and its cases can run, so a failure to describe
	// it only leaves the description empty.
	running, err := o.Client.Running(ctx)
	if err != nil {
		return LoadInfo{}, nil
	}
	for _, m := range running {
		if sameModel(m.Name, model) {
			return LoadInfo{Processor: processor(m.Size, m.SizeVRAM), ContextLength: m.ContextLength}, nil
		}
	}
	return LoadInfo{}, nil
}

// processor describes where a model of size bytes, sizeVRAM of them in GPU
// memory, runs, in the same words as `ollama ps`.
func processor(size, sizeVRAM int64) string {
	switch {
	case size <= 0 || sizeVRAM < 0 || sizeVRAM > size:
		return ""
	case sizeVRAM == 0:
		return "100% CPU"
	case sizeVRAM == size:
		return "100% GPU"
	}
	cpu := int(math.Round(float64(size-sizeVRAM) / float64(size) * 100))
	return fmt.Sprintf("%d%%/%d%% CPU/GPU", cpu, 100-cpu)
}

func (o *Ollama) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format, args...)
	}
}

func formatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
