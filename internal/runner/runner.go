// Package runner executes a suite's cases against its models and collects the results.
package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/blashkar/evalwright/internal/assert"
	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/suite"
)

// CheckResult is the outcome of one assertion on one output.
type CheckResult struct {
	Assertion string `json:"assertion"`
	Pass      bool   `json:"pass"`
	Detail    string `json:"detail,omitempty"`
}

// Result is one model's answer to one case, for one sample.
type Result struct {
	Case    string        `json:"case"`
	Model   string        `json:"model"`
	Sample  int           `json:"sample"`
	Output  string        `json:"output"`
	Latency time.Duration `json:"latency_ns"`
	Error   string        `json:"error,omitempty"`
	Checks  []CheckResult `json:"checks,omitempty"`
	Pass    bool          `json:"pass"`
}

// Report holds every result from one suite run.
type Report struct {
	Suite    string        `json:"suite"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration_ns"`
	Results  []Result      `json:"results"`
}

// Runner runs suites using the providers in its registry.
type Runner struct {
	Providers provider.Registry
}

type target struct {
	spec  string // as written in the suite, e.g. "ollama:qwen3:30b"
	model string // as the provider knows it, e.g. "qwen3:30b"
	p     provider.Provider
}

type preparedCase struct {
	suite.Case
	prompt  string
	asserts []assert.Assertion
}

// Run executes every case against every model in s.
//
// All models, prompts and assertions are checked before any model is called,
// so a typo fails in milliseconds instead of halfway through a long run.
// If ctx is cancelled, Run returns the results collected so far along with ctx's error.
func (r *Runner) Run(ctx context.Context, s *suite.Suite) (*Report, error) {
	targets, cases, err := r.prepare(s)
	if err != nil {
		return nil, err
	}

	rep := &Report{Suite: s.Name, Started: time.Now()}
	defer func() { rep.Duration = time.Since(rep.Started) }()

	// Models are the outer loop so each local model is loaded into memory once,
	// rather than being swapped in and out for every case.
	for _, t := range targets {
		for _, c := range cases {
			for sample := range s.Samples {
				res := runOne(ctx, t, s.System, c, sample+1)
				if ctx.Err() != nil {
					// The last result was cut short by cancellation; it says nothing about the model.
					return rep, ctx.Err()
				}
				rep.Results = append(rep.Results, res)
			}
		}
	}
	return rep, nil
}

func (r *Runner) prepare(s *suite.Suite) ([]target, []preparedCase, error) {
	var errs []error

	targets := make([]target, 0, len(s.Models))
	for _, spec := range s.Models {
		p, model, err := r.Providers.Resolve(spec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		targets = append(targets, target{spec: spec, model: model, p: p})
	}

	cases := make([]preparedCase, 0, len(s.Cases))
	for _, c := range s.Cases {
		prompt, err := s.Render(c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pc := preparedCase{Case: c, prompt: prompt}
		for _, spec := range c.Assert {
			a, err := assert.Build(spec.Type, spec.Decode)
			if err != nil {
				errs = append(errs, fmt.Errorf("case %q, line %d: %s: %w", c.Name, spec.Line, spec.Type, err))
				continue
			}
			pc.asserts = append(pc.asserts, a)
		}
		cases = append(cases, pc)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	return targets, cases, nil
}

func runOne(ctx context.Context, t target, system string, c preparedCase, sample int) Result {
	res := Result{Case: c.Name, Model: t.spec, Sample: sample}

	resp, err := t.p.Generate(ctx, provider.Request{Model: t.model, System: system, Prompt: c.prompt})
	if err != nil {
		// A failed request is a failed case, not a fatal error: one flaky
		// request shouldn't throw away the rest of the run.
		res.Error = err.Error()
		return res
	}
	res.Output = resp.Text
	res.Latency = resp.Latency

	out := assert.Output{Text: resp.Text, Latency: resp.Latency}
	res.Pass = true
	for _, a := range c.asserts {
		r := a.Check(ctx, out)
		res.Checks = append(res.Checks, CheckResult{Assertion: a.Name(), Pass: r.Pass, Detail: r.Detail})
		res.Pass = res.Pass && r.Pass
	}
	return res
}
