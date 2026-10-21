package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

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
			cancel() // simulate Ctrl-C during the second request
		}
		return "Paris", nil
	}}
	r := Runner{Providers: provider.Registry{"mock": m}}

	rep, err := r.Run(ctx, mustParse(t, twoModelSuite))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if rep == nil || len(rep.Results) != 1 {
		t.Fatalf("want 1 partial result kept, got %+v", rep)
	}
}
