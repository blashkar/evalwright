package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/baseline"
	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/stats"
)

func sampleReport() *runner.Report {
	return &runner.Report{
		Suite:    "demo",
		Duration: 1500 * time.Millisecond,
		Results: []runner.Result{
			{
				Case: "ok", Model: "mock:a", Sample: 1, Output: "Paris", Latency: 20 * time.Millisecond, Pass: true,
				Checks: []runner.CheckResult{{Assertion: `equals "Paris"`, Pass: true}},
			},
			{
				Case: "wrong", Model: "mock:a", Sample: 1, Output: "Lyon", Latency: 40 * time.Millisecond,
				Checks: []runner.CheckResult{{Assertion: `equals "Paris"`, Detail: `output "Lyon" does not equal "Paris"`}},
			},
			{Case: "down", Model: "mock:a", Sample: 1, Error: "connection refused"},
		},
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sampleReport(), nil); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"Suite: demo",
		"✗ wrong · mock:a · sample 1",
		`equals "Paris": output "Lyon" does not equal "Paris"`,
		`output: "Lyon"`,
		"error: connection refused",
		"1/3",
		"33%",
		"6–79%", // the 95% interval for 1 pass out of 3
		// p50 and p95 of the 2 answered requests; the error isn't counted.
		"20ms         40ms",
		"FAIL  demo in 1.5s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q\n--- output ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "Passed, by case") {
		t.Errorf("one model and one sample per case needs no per-case table:\n%s", got)
	}
	// A request that got no answer has no latency, not a latency of 0s.
	for line := range strings.Lines(got) {
		if f := strings.Fields(line); len(f) == 5 && f[0] == "down" && f[4] != "-" {
			t.Errorf("error row latency = %q, want -", f[4])
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, []*runner.Report{sampleReport()}); err != nil {
		t.Fatal(err)
	}
	var back []runner.Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(back) != 1 || len(back[0].Results) != 3 || back[0].Results[2].Error != "connection refused" {
		t.Errorf("round-tripped report = %+v", back)
	}
}

func TestExcerptTruncates(t *testing.T) {
	long := strings.Repeat("é", maxOutputExcerpt+10)
	got := excerpt(long)
	if want := maxOutputExcerpt + 1; len([]rune(got)) != want { // +1 for the ellipsis
		t.Errorf("excerpt has %d runes, want %d", len([]rune(got)), want)
	}
}

func TestTableInterrupted(t *testing.T) {
	tests := []struct {
		name string
		rep  *runner.Report
	}{
		{"before any result", &runner.Report{Suite: "demo", Interrupted: true}},
		{"after some passed", &runner.Report{Suite: "demo", Interrupted: true, Results: []runner.Result{{Case: "a", Model: "m", Sample: 1, Pass: true}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Table(&buf, tt.rep, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), "INTERRUPTED  demo") || strings.Contains(buf.String(), "PASS  demo") {
				t.Errorf("an interrupted run must say INTERRUPTED, never PASS:\n%s", buf.String())
			}
		})
	}
}

// twoModels has models a and b answer one case per entry of aPasses and
// bPasses, 5 samples each, passing the given number of samples.
func twoModels(aPasses, bPasses []int) *runner.Report {
	rep := &runner.Report{Suite: "cmp"}
	for _, m := range []struct {
		name   string
		passes []int
	}{{"a", aPasses}, {"b", bPasses}} {
		for c, n := range m.passes {
			for i := range 5 {
				rep.Results = append(rep.Results, runner.Result{
					Case: fmt.Sprintf("c%d", c+1), Model: m.name, Sample: i + 1, Pass: i < n,
				})
			}
		}
	}
	return rep
}

func TestTablePerCaseAndComparison(t *testing.T) {
	tests := []struct {
		name         string
		aPass, bPass []int
		want         []string
		notWant      []string
	}{
		{
			// The real llama3.2 vs qwen3 run: equal totals, opposite strengths.
			name:  "no difference",
			aPass: []int{4, 5, 5, 3, 4}, bPass: []int{1, 5, 5, 5, 5},
			want: []string{
				"c1    4/5 (38–96%)",
				"Compared with a (the first model), case by case:",
				"b: no significant difference (0 points, 95% interval -46 to +46).",
				"Only 5 cases",
			},
			// 4/5 vs 1/5 sounds decisive, but isn't (p = 0.21).
			notWant: []string{"better on", "worse on"},
		},
		{
			// 5/5 vs 0/5 is significant even after correcting for 6 cases
			// (p = 0.048); 5/5 vs 1/5 is not (p = 0.095).
			name:  "clearly better",
			aPass: []int{0, 1, 0, 0, 1, 0}, bPass: []int{5, 5, 5, 5, 5, 5},
			want: []string{
				"b passes more often: +93 points (95% interval +73 to +100).",
				"better on c1: 5/5 vs 0/5 (p = 0.048)",
				"better on c6: 5/5 vs 0/5 (p = 0.048)",
			},
			notWant: []string{"better on c2", "better on c5", "worse on"},
		},
		{
			name:  "identical",
			aPass: []int{5, 0, 5}, bPass: []int{5, 0, 5},
			want: []string{"b: same pass count on every case."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Table(&buf, twoModels(tt.aPass, tt.bPass), nil); err != nil {
				t.Fatal(err)
			}
			got := buf.String()
			for _, want := range append(tt.want, "Passed, by case:") {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			for _, bad := range tt.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("unexpected %q:\n%s", bad, got)
				}
			}
		})
	}
}

func TestTableSpeed(t *testing.T) {
	rep := &runner.Report{
		Suite: "speed",
		Loads: []runner.ModelLoad{{Model: "ollama:m", Time: 8651 * time.Millisecond}},
		Results: []runner.Result{
			{Case: "c", Model: "ollama:m", Sample: 1, Pass: true, Latency: 595 * time.Millisecond, Metrics: provider.Metrics{
				TimeToFirstToken: 13 * time.Millisecond, OutputTokens: 56, GenerationDuration: 560 * time.Millisecond,
			}},
			{Case: "c", Model: "ollama:down", Sample: 1, Error: "connection refused"},
		},
	}
	var buf bytes.Buffer
	if err := Table(&buf, rep, nil); err != nil {
		t.Fatal(err)
	}
	var speed [][]string
	for line := range strings.Lines(buf.String()) {
		if f := strings.Fields(line); len(f) == 7 && strings.HasPrefix(f[0], "ollama:") {
			speed = append(speed, f)
		}
	}
	want := [][]string{
		{"ollama:m", "595ms", "595ms", "13ms", "56", "100", "8.651s"},
		{"ollama:down", "-", "-", "-", "-", "-", "-"}, // no answers and never loaded
	}
	if fmt.Sprint(speed) != fmt.Sprint(want) {
		t.Errorf("speed rows = %v, want %v\n%s", speed, want, buf.String())
	}
}

func TestTableBaseline(t *testing.T) {
	rep := twoModels([]int{5, 0, 5}, []int{5, 5, 5})
	gate := &Gate{
		Baseline: &baseline.Comparison{
			Suite: "cmp",
			Models: []baseline.ModelResult{
				{
					Model: "a", Diff: stats.Diff{Mean: -1.0 / 3, Lo: -0.9, Hi: 0.2, Cases: 3},
					Digest: "sha256:0123456789abcdef", BaseDigest: "sha256:fedcba9876543210",
					LatencyP50: 120 * time.Millisecond, BaseLatencyP50: 100 * time.Millisecond,
				},
				{Model: "b", New: true},
			},
			Cases: []baseline.CaseResult{
				{Model: "a", Case: "c1", Status: baseline.Identical, Now: stats.Tally{Passed: 5, Total: 5}, Base: stats.Tally{Passed: 5, Total: 5}},
				{
					Model: "a", Case: "c2", Status: baseline.Regressed, P: 0.012, Changes: []string{"prompt", "model build"},
					Now: stats.Tally{Passed: 0, Total: 5}, Base: stats.Tally{Passed: 5, Total: 5},
				},
				{Model: "a", Case: "c3", Status: baseline.Unchanged, Now: stats.Tally{Passed: 5, Total: 5}, Base: stats.Tally{Passed: 5, Total: 5}},
				{Model: "b", Case: "c1", Status: baseline.New, Now: stats.Tally{Passed: 5, Total: 5}},
			},
			RemovedModels: []string{"old"},
		},
		Failures: []string{"a regressed on c2: 0/5, was 5/5 (p = 0.012)"},
	}
	var buf bytes.Buffer
	if err := Table(&buf, rep, gate); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"Compared with the baseline:",
		"a: no significant change (-33 points, 95% interval -90 to +20).",
		"model build changed: 0123456789ab, was fedcba987654",
		"median latency 120ms, was 100ms (+20%)",
		"b: new, not in the baseline.",
		"old: in the baseline, not run this time.",
		"c2    a      REGRESSED (p = 0.012)  0/5  5/5       prompt, model build",
		"c3    a      unchanged              5/5  5/5       answers only",
		"c1    b      new                    5/5  -         -",
		"The other 1 gave the same answers as in the baseline.",
		"Failed because:\n  - a regressed on c2: 0/5, was 5/5 (p = 0.012)",
		"FAIL  cmp",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}

	// The same results pass when the gate finds nothing wrong, although
	// model a fails a case.
	buf.Reset()
	if err := Table(&buf, rep, &Gate{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "PASS  cmp") {
		t.Errorf("want PASS:\n%s", buf.String())
	}
}
