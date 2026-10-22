// Package assert implements the checks evalwright runs against model output.
package assert

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Output is what a model produced for one case, as seen by assertions.
type Output struct {
	Text    string
	Latency time.Duration
}

// Result is the outcome of one assertion.
type Result struct {
	Pass   bool
	Detail string // why the check failed; empty when it passed
}

// Assertion checks one property of a model's output.
type Assertion interface {
	// Name describes the assertion for reports, e.g. `contains "Settings"`.
	Name() string
	// Check takes a context because later assertions (LLM judges, embeddings)
	// call a model themselves.
	Check(ctx context.Context, out Output) Result
}

// A builder creates an assertion from its YAML value. decode fills its
// argument with that value, so each builder chooses the type it expects.
type builder func(decode func(any) error) (Assertion, error)

var builders = map[string]builder{
	"equals":         newEquals,
	"contains":       newContains(false, false),
	"icontains":      newContains(false, true),
	"not_contains":   newContains(true, false),
	"regex":          newRegex(false),
	"not_regex":      newRegex(true),
	"max_length":     newMaxLength,
	"max_latency_ms": newMaxLatency,
}

// Build creates the assertion of type typ, reading its configuration with decode.
func Build(typ string, decode func(any) error) (Assertion, error) {
	b, ok := builders[typ]
	if !ok {
		available := slices.Sorted(maps.Keys(builders))
		return nil, fmt.Errorf("unknown assertion %q (available: %s)", typ, strings.Join(available, ", "))
	}
	return b(decode)
}

func pass() Result {
	return Result{Pass: true}
}

func fail(format string, args ...any) Result {
	return Result{Detail: fmt.Sprintf(format, args...)}
}
