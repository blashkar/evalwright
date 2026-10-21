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
