package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/runner"
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
	if err := Table(&buf, sampleReport()); err != nil {
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
			if err := Table(&buf, tt.rep); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), "INTERRUPTED  demo") || strings.Contains(buf.String(), "PASS  demo") {
				t.Errorf("an interrupted run must say INTERRUPTED, never PASS:\n%s", buf.String())
			}
		})
	}
}

// twoModels has model a passing 4 of 5 samples and b passing 1 of 5, all on one case.
func twoModels(aPasses, bPasses int) *runner.Report {
	rep := &runner.Report{Suite: "cmp"}
	for i := range 5 {
		rep.Results = append(rep.Results,
			runner.Result{Case: "c", Model: "a", Sample: i + 1, Pass: i < aPasses},
			runner.Result{Case: "c", Model: "b", Sample: i + 1, Pass: i < bPasses})
	}
	return rep
}

func TestTablePerCaseAndComparison(t *testing.T) {
	tests := []struct {
		name           string
		aPass, bPass   int
		wantComparison string
	}{
		// 4/5 vs 1/5 sounds decisive, but with 5 samples each the intervals
		// (38–96% and 4–62%) overlap.
		{"too few samples to tell apart", 4, 1, "The 95% intervals of a and b overlap"},
		{"clearly different", 5, 0, "a passes more often than b, beyond the 95% intervals."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Table(&buf, twoModels(tt.aPass, tt.bPass)); err != nil {
				t.Fatal(err)
			}
			got := buf.String()
			if !strings.Contains(got, "Passed, by case:") || !strings.Contains(got, fmt.Sprintf("c     %d/5  %d/5", tt.aPass, tt.bPass)) {
				t.Errorf("missing per-case table:\n%s", got)
			}
			if !strings.Contains(got, tt.wantComparison) {
				t.Errorf("missing %q:\n%s", tt.wantComparison, got)
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
	if err := Table(&buf, rep); err != nil {
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
