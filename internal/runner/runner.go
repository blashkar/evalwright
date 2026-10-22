// Package runner executes a suite's cases against its models and collects the results.
package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
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
	// Seed is the seed this sample was generated with, if the suite sets one,
	// so a surprising answer can be reproduced exactly.
	Seed *int64 `json:"seed,omitempty"`
	// Metrics are token counts and timings, for providers that report them.
	Metrics provider.Metrics `json:"metrics"`
	Error   string           `json:"error,omitempty"`
	Checks  []CheckResult    `json:"checks,omitempty"`
	Pass    bool             `json:"pass"`
}

// Report holds every result from one suite run.
type Report struct {
	Suite    string        `json:"suite"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration_ns"`
	// Interrupted is set when the run was cancelled before every case ran.
	Interrupted bool `json:"interrupted,omitempty"`
	// Loads records how long each model took to load before its cases ran,
	// for providers that load models.
	Loads   []ModelLoad `json:"loads,omitempty"`
	Results []Result    `json:"results"`
}

// ModelLoad is the time it took to load one model into memory. It is close
// to zero if the model was already loaded.
type ModelLoad struct {
	Model string        `json:"model"`
	Time  time.Duration `json:"time_ns"`
}

// Runner runs suites using the providers in its registry.
type Runner struct {
	Providers provider.Registry

	// Parallel is the maximum number of requests sent to one model at a time.
	// Values below 1 mean 1. Keep it at or below what the server can actually
	// run in parallel (OLLAMA_NUM_PARALLEL): extra requests wait in the
	// server's queue, and that wait would be measured as model latency.
	Parallel int

	// CaseTimeout limits each model request, and loading each model.
	// Zero means no limit.
	CaseTimeout time.Duration

	// OnModel, if set, is called when the runner starts on a model.
	OnModel func(model string)

	// OnResult, if set, is called after each result. Calls never overlap, so
	// it doesn't need to be safe for concurrent use.
	OnResult func(Result)
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

// job is one request: a case, for one sample.
type job struct {
	c      *preparedCase
	sample int
}

// Run executes every case against every model in s.
//
// Model names, prompts and assertions are all checked, and every model is
// prepared (e.g. confirmed to be downloaded), before any case runs, so a typo
// or missing model fails in seconds instead of halfway through a long run.
// If ctx is cancelled, Run returns the results finished so far along with ctx's error.
func (r *Runner) Run(ctx context.Context, s *suite.Suite) (*Report, error) {
	targets, cases, err := r.prepare(ctx, s)
	if err != nil {
		return nil, err
	}

	rep := &Report{Suite: s.Name, Started: time.Now()}
	defer func() { rep.Duration = time.Since(rep.Started) }()

	// Models are the outer loop so each local model is loaded into memory once,
	// rather than being swapped in and out for every case.
	for _, t := range targets {
		if r.OnModel != nil {
			r.OnModel(t.spec)
		}
		results, load, err := r.runModel(ctx, t, s, cases)
		if load != nil {
			rep.Loads = append(rep.Loads, *load)
		}
		rep.Results = append(rep.Results, results...)
		if err != nil {
			rep.Interrupted = true
			return rep, err
		}
	}
	return rep, nil
}

// Check reports the problems in s that can be found without contacting a
// model: unknown providers, prompts that don't render and invalid
// assertions. Run does the same checks, but calling Check on every suite
// first finds a mistake in the last suite before the others run.
func (r *Runner) Check(s *suite.Suite) error {
	_, _, err := r.compile(s)
	return err
}

func (r *Runner) prepare(ctx context.Context, s *suite.Suite) ([]target, []preparedCase, error) {
	targets, cases, err := r.compile(s)
	if err != nil {
		return nil, nil, err
	}

	// Only contact providers once the suite itself is known to be valid.
	var errs []error
	for _, t := range targets {
		if p, ok := t.p.(provider.Preparer); ok {
			if err := p.Prepare(ctx, t.model); err != nil {
				errs = append(errs, fmt.Errorf("model %q: %w", t.spec, err))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	return targets, cases, nil
}

// compile resolves s's models and builds its prompts and assertions.
func (r *Runner) compile(s *suite.Suite) ([]target, []preparedCase, error) {
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

// runModel runs every case and sample against one model, up to r.Parallel at
// a time, and returns the finished results in suite order, and how long the
// model took to load if its provider loads models.
func (r *Runner) runModel(ctx context.Context, t target, s *suite.Suite, cases []preparedCase) ([]Result, *ModelLoad, error) {
	var jobs []job
	for i := range cases {
		for n := range s.Samples {
			jobs = append(jobs, job{c: &cases[i], sample: n + 1})
		}
	}

	var load *ModelLoad
	if _, ok := t.p.(provider.Warmer); ok {
		start := time.Now()
		if err := r.warm(ctx, t, s); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return r.failAll(t, jobs, "loading model: "+err.Error()), nil, nil
		}
		load = &ModelLoad{Model: t.spec, Time: time.Since(start)}
	}

	// Each goroutine writes only its own index, so the slices need no lock,
	// and results come out in suite order however the requests interleave.
	results := make([]Result, len(jobs))
	finished := make([]bool, len(jobs))
	sem := make(chan struct{}, max(r.Parallel, 1))
	var wg sync.WaitGroup
	var reportMu sync.Mutex

	for i, j := range jobs {
		sem <- struct{}{} // wait for a free slot
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			res := r.runOne(ctx, t, s, j)
			if res.Error != "" && ctx.Err() != nil {
				// Probably cut short by cancellation, which says nothing about
				// the model. An answer that finished before it is still kept.
				return
			}
			results[i], finished[i] = res, true
			reportMu.Lock()
			r.report(res)
			reportMu.Unlock()
		})
	}
	wg.Wait()

	out := make([]Result, 0, len(jobs))
	for i, ok := range finished {
		if ok {
			out = append(out, results[i])
		}
	}
	return out, load, ctx.Err()
}

// failAll fails every job with msg. A model that can't load (e.g. not enough
// memory) fails all of its cases, but the other models in the suite still run.
func (r *Runner) failAll(t target, jobs []job, msg string) []Result {
	out := make([]Result, len(jobs))
	for i, j := range jobs {
		out[i] = Result{Case: j.c.Name, Model: t.spec, Sample: j.sample, Error: msg}
		r.report(out[i])
	}
	return out
}

// warm loads t's model, so the load time isn't counted as the first case's
// latency. It has the same time limit as a request, so a model that never
// finishes loading can't hang the run. t's provider must be a Warmer.
func (r *Runner) warm(ctx context.Context, t target, s *suite.Suite) error {
	w := t.p.(provider.Warmer)
	if r.CaseTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.CaseTimeout)
		defer cancel()
	}
	err := w.Warm(ctx, t.model, s.Options)
	if r.CaseTimeout > 0 && errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		return fmt.Errorf("timed out after %s", r.CaseTimeout)
	}
	return err
}

func (r *Runner) report(res Result) {
	if r.OnResult != nil {
		r.OnResult(res)
	}
}

func (r *Runner) runOne(ctx context.Context, t target, s *suite.Suite, j job) Result {
	res := Result{Case: j.c.Name, Model: t.spec, Sample: j.sample}

	opts := s.Options
	if seed, ok := s.SampleSeed(j.sample); ok {
		// Copy rather than modify: every goroutine shares s.Options.
		opts = maps.Clone(s.Options)
		opts["seed"] = seed
		res.Seed = &seed
	}

	reqCtx := ctx
	if r.CaseTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, r.CaseTimeout)
		defer cancel()
	}

	req := provider.Request{Model: t.model, System: s.System, Prompt: j.c.prompt, Options: opts}
	resp, err := t.p.Generate(reqCtx, req)
	if err != nil {
		// A failed request is a failed case, not a fatal error: one slow or
		// flaky request shouldn't throw away the rest of the run.
		res.Error = err.Error()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			res.Error = fmt.Sprintf("timed out after %s", r.CaseTimeout)
		}
		return res
	}
	res.Output = resp.Text
	res.Latency = resp.Latency
	res.Metrics = resp.Metrics

	out := assert.Output{Text: resp.Text, Latency: resp.Latency}
	res.Pass = true
	for _, a := range j.c.asserts {
		cr := a.Check(ctx, out)
		res.Checks = append(res.Checks, CheckResult{Assertion: a.Name(), Pass: cr.Pass, Detail: cr.Detail})
		res.Pass = res.Pass && cr.Pass
	}
	return res
}
