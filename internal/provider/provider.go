// Package provider defines how evalwright talks to language models.
package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Request is a single prompt sent to a model.
type Request struct {
	Model  string
	System string
	Prompt string
	// Options are model parameters such as temperature and seed. Providers
	// pass them through and ignore any they don't support.
	Options map[string]any
}

// Response is a model's answer to a Request.
type Response struct {
	Text    string
	Latency time.Duration
	// Metrics are the provider's measurements of the request. Fields a
	// provider can't measure are left at zero.
	Metrics Metrics
}

// Metrics describe how a model spent its time on one request.
type Metrics struct {
	// TimeToFirstToken is measured by the client, from sending the request
	// to receiving the first generated token. It is what a user waits
	// before text starts to appear.
	TimeToFirstToken time.Duration `json:"time_to_first_token_ns,omitempty"`
	// PromptTokens and PromptDuration cover reading the input. A prompt the
	// server has cached is processed much faster than a new one.
	PromptTokens   int           `json:"prompt_tokens,omitempty"`
	PromptDuration time.Duration `json:"prompt_ns,omitempty"`
	// OutputTokens and GenerationDuration cover writing the answer.
	OutputTokens       int           `json:"output_tokens,omitempty"`
	GenerationDuration time.Duration `json:"generation_ns,omitempty"`
}

// TokensPerSecond is the generation speed, or 0 if it wasn't measured.
func (m Metrics) TokensPerSecond() float64 {
	if m.OutputTokens == 0 || m.GenerationDuration <= 0 {
		return 0
	}
	return float64(m.OutputTokens) / m.GenerationDuration.Seconds()
}

// Provider generates text from a model. Implementations must honor ctx
// cancellation so a run can be stopped with Ctrl-C.
type Provider interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// Preparer is implemented by providers that must check or set up a model
// before it is used, for example making sure it is downloaded. The runner
// prepares every model before running any case, so a missing model fails
// the run immediately. Prepare returns what the provider knows about the
// model.
type Preparer interface {
	Prepare(ctx context.Context, model string) (ModelInfo, error)
}

// ModelInfo identifies the exact build of a model, and the software that
// runs it. A name such as llama3.2:3b can point to a different build after
// a new download, so results record the digest to show which build
// produced them. Fields a provider doesn't know are left empty.
type ModelInfo struct {
	Digest        string `json:"digest,omitempty"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	// Defaults are the model's own values for options a request doesn't
	// set, such as {"temperature": 0.7, "top_k": 20}. Models ship with
	// different defaults, so two models asked the same way can sample
	// differently.
	Defaults map[string]any `json:"defaults,omitempty"`
	// Engine is the server software and its version, e.g. "ollama 0.32.15".
	// A seed only reproduces an answer on the same engine: a new version
	// can compute the same model's probabilities slightly differently.
	Engine string `json:"engine,omitempty"`
}

// Warmer is implemented by providers that can load a model ahead of time.
// The runner warms each model just before its cases run, so the time to
// load it isn't counted as the first case's latency. options are the
// suite's options, since some of them can change how a model is loaded.
// Warm returns how the model was loaded.
type Warmer interface {
	Warm(ctx context.Context, model string, options map[string]any) (LoadInfo, error)
}

// LoadInfo describes how a loaded model runs: server settings, rather than
// the suite's, that can change its answers. Running the same model on a GPU
// instead of the CPU, or with a different context window, uses different
// arithmetic, so a seed that reproduced an answer before may not. Fields a
// provider doesn't know are left empty.
type LoadInfo struct {
	// Processor is where the model runs, like Ollama's `ollama ps`:
	// "100% GPU", "100% CPU" or "25%/75% CPU/GPU".
	Processor string `json:"processor,omitempty"`
	// ContextLength is the context window the model was loaded with.
	ContextLength int `json:"context_length,omitempty"`
}

// Registry maps a provider name to its implementation. Suites refer to models
// as "provider:model", e.g. "ollama:qwen3:30b".
type Registry map[string]Provider

// Resolve splits a model spec at its first colon and returns the matching
// provider and the model name that provider understands.
func (r Registry) Resolve(spec string) (Provider, string, error) {
	name, model, ok := strings.Cut(spec, ":")
	if !ok || name == "" || model == "" {
		return nil, "", fmt.Errorf("model %q: want provider:model, for example ollama:qwen3:30b", spec)
	}
	p, ok := r[name]
	if !ok {
		available := slices.Sorted(maps.Keys(r))
		return nil, "", fmt.Errorf("model %q: unknown provider %q (available: %s)", spec, name, strings.Join(available, ", "))
	}
	return p, model, nil
}
