package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/suite"
)

func mustParse(t *testing.T, src string) *suite.Suite {
	t.Helper()
	s, err := suite.Parse(strings.NewReader(src), "test.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

const twoModelSuite = `
name: capitals
models: [mock:good, mock:bad]
prompt: "Capital of {{.country}}?"
samples: 2
cases:
  - name: france
    vars: { country: France }
    assert:
      - equals: Paris
`

// capitals answers correctly for model "good" and wrongly for anything else.
func capitals(req provider.Request) (string, error) {
	if req.Model == "good" {
		return "Paris", nil
	}
	return "Lyon", nil
}

func TestRun(t *testing.T) {
	r := Runner{Providers: provider.Registry{"mock": &provider.Mock{Reply: capitals}}}
	rep, err := r.Run(context.Background(), mustParse(t, twoModelSuite))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(rep.Results); got != 4 { // 2 models × 1 case × 2 samples
		t.Fatalf("got %d results, want 4", got)
	}
	if rep.Passed() {
		t.Error("Passed() = true, want false because mock:bad fails")
	}

	sum := rep.Summary()
	if len(sum) != 2 {
		t.Fatalf("got %d model summaries, want 2", len(sum))
	}
	if sum[0].Model != "mock:good" || sum[0].Passed != 2 || sum[0].Total != 2 {
		t.Errorf("summary[0] = %+v, want mock:good 2/2", sum[0])
	}
	if sum[1].Model != "mock:bad" || sum[1].Passed != 0 || sum[1].PassRate() != 0 {
		t.Errorf("summary[1] = %+v, want mock:bad 0/2", sum[1])
	}
	if d := rep.Results[2].Checks[0].Detail; !strings.Contains(d, `"Lyon"`) {
		t.Errorf("failure detail = %q, want it to mention the wrong answer", d)
	}
}

func TestRunProviderErrorFailsCase(t *testing.T) {
	failing := &provider.Mock{Reply: func(provider.Request) (string, error) {
		return "", errors.New("connection refused")
	}}
	r := Runner{Providers: provider.Registry{"mock": failing}}
	rep, err := r.Run(context.Background(), mustParse(t, twoModelSuite))
	if err != nil {
		t.Fatalf("Run: %v (a provider error should fail the case, not the run)", err)
	}
	if res := rep.Results[0]; res.Pass || res.Error != "connection refused" {
		t.Errorf("result = %+v, want failed with provider error", res)
	}
}

func TestRunValidatesBeforeCallingModels(t *testing.T) {
	calls := 0
	m := &provider.Mock{Reply: func(provider.Request) (string, error) {
		calls++
		return "", nil
	}}
	r := Runner{Providers: provider.Registry{"mock": m}}

	s := mustParse(t, `
name: broken
models: [mock:a, ollama:missing]
prompt: hi
cases:
  - assert:
      - regex: "("
      - has_text: typo
`)
	_, err := r.Run(context.Background(), s)
	if err == nil {
		t.Fatal("Run succeeded, want error")
	}
	for _, want := range []string{`unknown provider "ollama"`, "regex", `unknown assertion "has_text"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if calls != 0 {
		t.Errorf("model was called %d times before validation failed, want 0", calls)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	m := &provider.Mock{Reply: func(provider.Request) (string, error) {
		calls++
		if calls == 2 {
			cancel() // simulate Ctrl-C cutting the second request short
			return "", context.Canceled
		}
		return "Paris", nil
	}}
	r := Runner{Providers: provider.Registry{"mock": m}}

	rep, err := r.Run(ctx, mustParse(t, twoModelSuite))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	// The cut-short request says nothing about the model, so it is dropped.
	if rep == nil || len(rep.Results) != 1 || rep.Results[0].Error != "" {
		t.Fatalf("want only the 1 finished result, got %+v", rep)
	}
	if !rep.Interrupted || rep.Passed() {
		t.Errorf("Interrupted = %v, Passed = %v; want an interrupted run that doesn't pass", rep.Interrupted, rep.Passed())
	}
}

func TestRunCancelledKeepsAnswerThatFinished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	m := &provider.Mock{Reply: func(provider.Request) (string, error) {
		calls++
		if calls == 2 {
			cancel() // Ctrl-C arrives, but this answer is already complete
		}
		return "Paris", nil
	}}
	r := Runner{Providers: provider.Registry{"mock": m}}

	rep, err := r.Run(ctx, mustParse(t, twoModelSuite))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if rep == nil || len(rep.Results) != 2 || !rep.Results[1].Pass {
		t.Fatalf("want both finished answers kept, got %+v", rep)
	}
	if !rep.Interrupted {
		t.Error("Interrupted = false, want true")
	}
}

func TestRunParallelKeepsOrderAndLimit(t *testing.T) {
	const parallel = 3
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	m := &provider.Mock{Reply: func(req provider.Request) (string, error) {
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()

		// Earlier cases take longer, so they finish last.
		n, _ := strconv.Atoi(req.Prompt)
		time.Sleep(time.Duration(10-n) * 10 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		return req.Prompt, nil
	}}

	var src strings.Builder
	src.WriteString("name: order\nmodels: [mock:a]\nprompt: \"{{.n}}\"\ncases:\n")
	for i := range 8 {
		fmt.Fprintf(&src, "  - {name: c%d, vars: {n: %d}, assert: [{equals: \"%d\"}]}\n", i, i, i)
	}

	var reported []string
	r := Runner{
		Providers: provider.Registry{"mock": m},
		Parallel:  parallel,
		OnResult:  func(res Result) { reported = append(reported, res.Case) },
	}
	rep, err := r.Run(context.Background(), mustParse(t, src.String()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if maxInFlight > parallel {
		t.Errorf("%d requests ran at once, limit is %d", maxInFlight, parallel)
	}
	if maxInFlight < 2 {
		t.Errorf("requests never overlapped (max in flight %d); runner is not concurrent", maxInFlight)
	}
	for i, res := range rep.Results {
		if want := fmt.Sprintf("c%d", i); res.Case != want || !res.Pass {
			t.Errorf("result %d = %s (pass %v), want %s passing; results must stay in suite order", i, res.Case, res.Pass, want)
		}
	}
	if len(reported) != 8 {
		t.Errorf("OnResult called %d times, want 8", len(reported))
	}
}

func TestRunPassesOptionsAndSystem(t *testing.T) {
	var got provider.Request
	m := &provider.Mock{Reply: func(req provider.Request) (string, error) {
		got = req
		return "ok", nil
	}}
	s := mustParse(t, `
name: opts
models: [mock:m1]
options: {temperature: 0, seed: 42}
system: Be brief.
prompt: hi
cases: [{assert: [{equals: ok}]}]
`)
	r := Runner{Providers: provider.Registry{"mock": m}}
	if _, err := r.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got.Model != "m1" || got.System != "Be brief." || got.Options["seed"] != int64(42) {
		t.Errorf("request = %+v", got)
	}
}

// lifecycleProvider records Prepare and Warm calls and can fail them.
type lifecycleProvider struct {
	provider.Mock
	events      []string
	missing     string // model Prepare reports as missing
	cannotLoad  string // model Warm fails to load
	slowLoad    bool   // Warm blocks until ctx is done
	warmOptions map[string]any
	generations int
}

func (p *lifecycleProvider) Prepare(_ context.Context, model string) error {
	p.events = append(p.events, "prepare "+model)
	if model == p.missing {
		return errors.New("not installed")
	}
	return nil
}

func (p *lifecycleProvider) Warm(ctx context.Context, model string, options map[string]any) error {
	p.events = append(p.events, "warm "+model)
	p.warmOptions = options
	if model == p.cannotLoad {
		return errors.New("out of memory")
	}
	if p.slowLoad {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (p *lifecycleProvider) Generate(ctx context.Context, req provider.Request) (provider.Response, error) {
	p.generations++
	return p.Mock.Generate(ctx, req)
}

func TestRunPreparesAllModelsFirstThenWarmsEach(t *testing.T) {
	p := &lifecycleProvider{}
	var started []string
	r := Runner{Providers: provider.Registry{"x": p}, OnModel: func(m string) { started = append(started, m) }}

	s := mustParse(t, "name: s\nmodels: [x:a, x:b]\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")
	if _, err := r.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare a", "prepare b", "warm a", "warm b"}
	if !slices.Equal(p.events, want) {
		t.Errorf("events = %v, want %v", p.events, want)
	}
	if !slices.Equal(started, []string{"x:a", "x:b"}) {
		t.Errorf("OnModel calls = %v", started)
	}
}

func TestRunMissingModelFailsBeforeAnyCase(t *testing.T) {
	p := &lifecycleProvider{missing: "b"}
	r := Runner{Providers: provider.Registry{"x": p}}
	s := mustParse(t, "name: s\nmodels: [x:a, x:b]\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")

	_, err := r.Run(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), `model "x:b": not installed`) {
		t.Fatalf("error = %v, want missing model error", err)
	}
	if p.generations != 0 {
		t.Errorf("%d cases ran before the missing model was reported", p.generations)
	}
}

func TestRunModelThatCannotLoadFailsOnlyItsCases(t *testing.T) {
	p := &lifecycleProvider{cannotLoad: "a"}
	r := Runner{Providers: provider.Registry{"x": p}}
	s := mustParse(t, "name: s\nmodels: [x:a, x:b]\nprompt: hi\nsamples: 2\ncases: [{assert: [{contains: hi}]}]\n")

	rep, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	sum := rep.Summary()
	if sum[0].Passed != 0 || sum[0].Total != 2 || sum[1].Passed != 2 {
		t.Errorf("summary = %+v, want x:a 0/2 and x:b 2/2", sum)
	}
	if e := rep.Results[0].Error; e != "loading model: out of memory" {
		t.Errorf("error = %q", e)
	}
}

func TestRunWarmsWithSuiteOptions(t *testing.T) {
	p := &lifecycleProvider{}
	r := Runner{Providers: provider.Registry{"x": p}}
	s := mustParse(t, "name: s\nmodels: [x:a]\noptions: {num_ctx: 8192}\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")
	if _, err := r.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if p.warmOptions["num_ctx"] != 8192 {
		t.Errorf("Warm options = %v, want the suite's num_ctx", p.warmOptions)
	}
}

func TestRunLoadTimeoutFailsOnlyThatModel(t *testing.T) {
	p := &lifecycleProvider{slowLoad: true}
	r := Runner{Providers: provider.Registry{"x": p}, CaseTimeout: 10 * time.Millisecond}
	s := mustParse(t, "name: s\nmodels: [x:a]\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")

	rep, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatalf("Run: %v (a load timeout should fail the model's cases, not the run)", err)
	}
	if e := rep.Results[0].Error; e != "loading model: timed out after 10ms" {
		t.Errorf("error = %q", e)
	}
	if p.generations != 0 {
		t.Errorf("%d cases were sent to a model that never loaded", p.generations)
	}
}

func TestRunCaseTimeout(t *testing.T) {
	slow := &provider.Mock{Reply: func(provider.Request) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "late", nil
	}}
	// Mock only checks ctx before replying, so wrap it to honor the deadline mid-request.
	r := Runner{Providers: provider.Registry{"mock": ctxAware{slow}}, CaseTimeout: 10 * time.Millisecond}
	s := mustParse(t, "name: s\nmodels: [mock:a]\nprompt: hi\ncases: [{assert: [{contains: late}]}]\n")

	rep, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatalf("Run: %v (a timeout should fail the case, not the run)", err)
	}
	if res := rep.Results[0]; res.Pass || res.Error != "timed out after 10ms" {
		t.Errorf("result = %+v, want timeout error", res)
	}
}

// ctxAware runs a provider in the background and returns as soon as ctx is done,
// the way a real streaming provider does.
type ctxAware struct{ p provider.Provider }

func (c ctxAware) Generate(ctx context.Context, req provider.Request) (provider.Response, error) {
	type out struct {
		resp provider.Response
		err  error
	}
	ch := make(chan out, 1)
	go func() {
		resp, err := c.p.Generate(context.WithoutCancel(ctx), req)
		ch <- out{resp, err}
	}()
	select {
	case o := <-ch:
		return o.resp, o.err
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
}

func TestRunDerivesSeedPerSample(t *testing.T) {
	var mu sync.Mutex
	seen := map[int]any{} // sample prompt index -> seed sent
	m := &provider.Mock{Reply: func(req provider.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen[len(seen)] = req.Options["seed"]
		return "ok", nil
	}}
	s := mustParse(t, `
name: seeds
models: [mock:a]
options: {temperature: 0.7, seed: 7}
samples: 3
prompt: hi
cases: [{assert: [{equals: ok}]}]
`)
	r := Runner{Providers: provider.Registry{"mock": m}, Parallel: 3}
	rep, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	for i, res := range rep.Results {
		if want := int64(7 + i); res.Seed == nil || *res.Seed != want {
			t.Errorf("sample %d recorded seed %v, want %d", res.Sample, res.Seed, want)
		}
	}
	var sent []int64
	for _, v := range seen {
		sent = append(sent, v.(int64))
	}
	slices.Sort(sent)
	if !slices.Equal(sent, []int64{7, 8, 9}) {
		t.Errorf("seeds sent to the model = %v, want [7 8 9]", sent)
	}
	if s.Options["seed"] != 7 {
		t.Errorf("suite options were modified: seed = %v", s.Options["seed"])
	}
}

func TestRunWithoutSeedSendsNone(t *testing.T) {
	var got provider.Request
	m := &provider.Mock{Reply: func(req provider.Request) (string, error) {
		got = req
		return "ok", nil
	}}
	s := mustParse(t, "name: s\nmodels: [mock:a]\noptions: {temperature: 0.7}\nprompt: hi\ncases: [{assert: [{equals: ok}]}]\n")
	rep, err := (&Runner{Providers: provider.Registry{"mock": m}}).Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Options["seed"]; ok || rep.Results[0].Seed != nil {
		t.Errorf("a seed was invented: options %v, recorded %v", got.Options, rep.Results[0].Seed)
	}
}

func TestSummaryLatencyIgnoresErrors(t *testing.T) {
	rep := &Report{Results: []Result{
		{Model: "m", Latency: 100 * time.Millisecond, Pass: true},
		{Model: "m", Latency: 300 * time.Millisecond},
		{Model: "m", Error: "timed out after 1s"}, // no answer, so no latency
		{Model: "down", Error: "connection refused"},
	}}
	sum := rep.Summary()
	if m := sum[0]; m.MeanLatency != 200*time.Millisecond || m.Errors != 1 || m.Total != 3 {
		t.Errorf("summary = %+v, want mean 200ms over the 2 answered requests and 1 error", m)
	}
	if d := sum[1]; d.MeanLatency != 0 || d.Errors != 1 {
		t.Errorf("summary for a model with no answers = %+v", d)
	}
}
