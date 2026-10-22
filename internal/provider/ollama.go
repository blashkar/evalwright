package provider

import (
	"context"
	"fmt"
	"io"
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
// AutoPull is set, downloads it when it isn't.
func (o *Ollama) Prepare(ctx context.Context, model string) error {
	_, err := o.Client.Show(ctx, model)
	switch {
	case err == nil:
		return nil
	case !ollama.IsNotFound(err):
		return err
	case !o.AutoPull:
		return fmt.Errorf("model %q is not installed; run `ollama pull %s` or pass --pull", model, model)
	}

	o.logf("Pulling %s...\n", model)
	var digest string
	lastPct := -1
	err = o.Client.Pull(ctx, model, func(p ollama.PullProgress) error {
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
	if err != nil {
		return fmt.Errorf("pulling %s: %w", model, err)
	}
	return nil
}

// Warm implements Warmer.
func (o *Ollama) Warm(ctx context.Context, model string, options map[string]any) error {
	return o.Client.Load(ctx, model, options)
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
