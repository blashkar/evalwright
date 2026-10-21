package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		"FAIL  demo in 1.5s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q\n--- output ---\n%s", want, got)
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
