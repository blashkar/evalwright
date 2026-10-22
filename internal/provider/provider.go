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
}

// Provider generates text from a model. Implementations must honor ctx
// cancellation so a run can be stopped with Ctrl-C.
type Provider interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// Preparer is implemented by providers that must check or set up a model
// before it is used, for example making sure it is downloaded. The runner
// prepares every model before running any case, so a missing model fails
// the run immediately.
type Preparer interface {
	Prepare(ctx context.Context, model string) error
}

// Warmer is implemented by providers that can load a model ahead of time.
// The runner warms each model just before its cases run, so the time to
// load it isn't counted as the first case's latency. options are the
// suite's options, since some of them can change how a model is loaded.
type Warmer interface {
	Warm(ctx context.Context, model string, options map[string]any) error
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
