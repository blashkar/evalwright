package baseline

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/stats"
	"github.com/blashkar/evalwright/internal/suite"
)

// run describes one model's results: the number of passing samples (out
// of 5) for each case, by case name.
type run map[string]int

// report builds a report in which each model answers its cases 5 times.
// Every case has the same fingerprint, and a passing sample answers "yes".
func report(models map[string]run, order ...string) *runner.Report {
	rep := &runner.Report{Suite: "s"}
	seen := map[string]bool{}
	for _, m := range order {
		for _, c := range sortedKeys(models[m]) {
			if !seen[c] {
				seen[c] = true
				rep.Cases = append(rep.Cases, runner.CaseInfo{Name: c, Fingerprint: runner.Fingerprint{Prompt: "p-" + c}})
			}
			for i := range 5 {
				pass := i < models[m][c]
				out := "no"
				if pass {
					out = "yes"
				}
				seed := int64(7 + i)
				rep.Results = append(rep.Results, runner.Result{
					Case: c, Model: m, Sample: i + 1, Output: out, Pass: pass, Seed: &seed,
					Latency: 100 * time.Millisecond,
				})
			}
		}
		rep.Models = append(rep.Models, runner.ModelInfo{Model: m})
	}
	return rep
}

func sortedKeys(r run) []string {
	var keys []string
	for k := range r {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func baselineOf(t *testing.T, rep *runner.Report) *File {
	t.Helper()
	f, err := FromReports([]*runner.Report{rep})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func statuses(cmp *Comparison) map[string]Status {
	out := map[string]Status{}
	for _, c := range cmp.Cases {
		out[c.Model+"/"+c.Case] = c.Status
	}
	return out
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.json")
	f := baselineOf(t, report(map[string]run{"m": {"a": 5, "b": 2}}, "m"))
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := got.Suite("s")
	if !ok || len(s.Models) != 1 || len(s.Models[0].Cases) != 2 {
		t.Fatalf("loaded %+v", got)
	}
	c := s.Models[0].Cases[1]
	if c.Case != "b" || c.Passed != 2 || c.Total != 5 || !slices.Equal(c.Seeds, []int64{7, 8, 9, 10, 11}) || c.Outputs == "" {
		t.Errorf("case b = %+v", c)
	}
	if s.Models[0].LatencyP50 != 100*time.Millisecond {
		t.Errorf("latency = %s", s.Models[0].LatencyP50)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"not a baseline file":     `[1, 2]`,
		"no version":              `{"suites": []}`,
		"newer than this":         `{"version": 99, "suites": [{"name": "s"}]}`,
		"baseline has no suites":  `{"version": 1}`,
		"no such file or directo": "",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestFromReportsRejectsInterruptedRun(t *testing.T) {
	rep := report(map[string]run{"m": {"a": 5}}, "m")
	rep.Interrupted = true
	if _, err := FromReports([]*runner.Report{rep}); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v", err)
	}
}

func TestCompare(t *testing.T) {
	base := baselineOf(t, report(map[string]run{
		"m":   {"same": 5, "fails": 0, "drop": 5, "rise": 0, "wobble": 4, "gone": 5},
		"old": {"same": 5},
	}, "m", "old"))

	now := report(map[string]run{
		"m":   {"same": 5, "fails": 0, "drop": 0, "rise": 5, "wobble": 3, "added": 1},
		"new": {"same": 5},
	}, "m", "new")
	cmp := Compare(base, now)

	want := map[string]Status{
		"m/same":   Identical,
		"m/fails":  Identical, // a known failure that didn't change
		"m/drop":   Regressed,
		"m/rise":   Improved,
		"m/wobble": Changed, // 4/5 to 3/5 is noise
		"m/added":  New,
		"m/gone":   Removed,
		"new/same": New,
	}
	if got := statuses(cmp); !mapsEqual(got, want) {
		t.Errorf("statuses = %v\nwant       %v", got, want)
	}
	if !slices.Equal(cmp.RemovedModels, []string{"old"}) {
		t.Errorf("removed models = %v", cmp.RemovedModels)
	}
	if len(cmp.Models) != 2 || cmp.Models[0].New || !cmp.Models[1].New {
		t.Errorf("models = %+v", cmp.Models)
	}
	for _, c := range cmp.Cases {
		if c.Case == "drop" && (c.P < 0.01 || c.P >= 0.05) {
			// one-sided 0/5 vs 5/5 is 0.004, times 5 shared cases (Holm)
			t.Errorf("drop p = %v, want about 0.02", c.P)
		}
	}
}

func mapsEqual(a, b map[string]Status) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestCompareSaysWhatChanged(t *testing.T) {
	base := baselineOf(t, report(map[string]run{"m": {"a": 5, "b": 5}}, "m"))
	base.Suites[0].Models[0].Digest = "sha256:old"

	now := report(map[string]run{"m": {"a": 5, "b": 5}}, "m")
	now.Models[0].Digest = "sha256:new"
	now.Cases[0].Fingerprint.Prompt = "edited"
	now.Results[len(now.Results)-1].Output = "yes, differently"

	cmp := Compare(base, now)
	if !cmp.Models[0].BuildChanged() || !cmp.Models[0].Same {
		t.Errorf("model = %+v, want a changed build and the same pass counts", cmp.Models[0])
	}
	got := map[string][]string{}
	for _, c := range cmp.Cases {
		if c.Status != Unchanged {
			t.Errorf("%s: status %s, want unchanged: same pass count, different inputs", c.Case, c.Status)
		}
		got[c.Case] = c.Changes
	}
	if !slices.Equal(got["a"], []string{"prompt", "model build"}) || !slices.Equal(got["b"], []string{"model build"}) {
		t.Errorf("changes = %v", got)
	}
}

func TestCompareSameInputsDifferentAnswers(t *testing.T) {
	base := baselineOf(t, report(map[string]run{"m": {"a": 5}}, "m"))
	now := report(map[string]run{"m": {"a": 5}}, "m")
	now.Results[0].Output = "yes!"
	if c := Compare(base, now).Cases[0]; c.Status != Unchanged || len(c.Changes) != 0 {
		t.Errorf("case = %+v, want unchanged with no input changes", c)
	}
}

func TestCompareMissingSuiteAndInterruptedRun(t *testing.T) {
	base := baselineOf(t, report(map[string]run{"m": {"a": 5, "b": 5}}, "m"))

	other := report(map[string]run{"m": {"a": 5}}, "m")
	other.Suite = "other"
	if cmp := Compare(base, other); !cmp.Missing || cmp.Cases[0].Status != New {
		t.Errorf("missing suite: %+v", cmp)
	}

	// An interrupted run didn't reach b; it wasn't removed from the suite.
	cut := report(map[string]run{"m": {"a": 5}}, "m")
	cut.Interrupted = true
	if got := statuses(Compare(base, cut)); len(got) != 1 {
		t.Errorf("interrupted run statuses = %v, want only a", got)
	}
}

func TestFailures(t *testing.T) {
	pct := func(p float64) *suite.Percent { v := suite.Percent(p); return &v }
	passing := report(map[string]run{"m": {"a": 5, "b": 5}}, "m")
	mixed := report(map[string]run{"m": {"a": 5, "b": 3}}, "m")

	tests := []struct {
		name  string
		rep   *runner.Report
		suite suite.Suite
		cmp   *Comparison
		want  []string // substrings, one per failure
	}{
		{name: "all pass", rep: passing},
		{name: "default rule", rep: mixed, want: []string{"2 of 10 results failed"}},
		{name: "min pass rate met", rep: mixed, suite: suite.Suite{MinPassRate: pct(0.8)}},
		{
			name: "min pass rate missed", rep: mixed, suite: suite.Suite{MinPassRate: pct(0.9)},
			want: []string{"m passed 80% of samples, below min_pass_rate 90%"},
		},
		{
			name: "known failure with baseline", rep: mixed,
			cmp: Compare(baselineOf(t, mixed), mixed),
		},
		{
			name: "regression", rep: report(map[string]run{"m": {"a": 0, "b": 5}}, "m"),
			cmp:  Compare(baselineOf(t, passing), report(map[string]run{"m": {"a": 0, "b": 5}}, "m")),
			want: []string{"m regressed on a: 0/5, was 5/5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Failures(tt.rep, &tt.suite, tt.cmp)
			if len(got) != len(tt.want) {
				t.Fatalf("failures = %q, want %d matching %q", got, len(tt.want), tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("failure %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

// A drop spread over many cases is a regression even when no single case
// is significant on its own.
func TestFailuresOverallDrop(t *testing.T) {
	before, after := run{}, run{}
	for _, c := range strings.Split("abcdefghijklmnopqrst", "") {
		before[c], after[c] = 5, 3
	}
	base := baselineOf(t, report(map[string]run{"m": before}, "m"))
	now := report(map[string]run{"m": after}, "m")
	got := Failures(now, &suite.Suite{}, Compare(base, now))
	if len(got) != 1 || !strings.Contains(got[0], "m passes less often than in the baseline: -40 points") {
		t.Errorf("failures = %q", got)
	}
}

func TestFailuresLatency(t *testing.T) {
	limit := suite.Percent(0.2)
	base := baselineOf(t, report(map[string]run{"m": {"a": 5}}, "m"))
	now := report(map[string]run{"m": {"a": 5}}, "m")
	for i := range now.Results {
		now.Results[i].Latency = 130 * time.Millisecond
	}
	cmp := Compare(base, now)
	if got := Failures(now, &suite.Suite{}, cmp); len(got) != 0 {
		t.Errorf("without max_latency_increase, latency never fails: %q", got)
	}
	got := Failures(now, &suite.Suite{MaxLatencyIncrease: &limit}, cmp)
	if len(got) != 1 || !strings.Contains(got[0], "m median latency is 130ms, 30% slower than the baseline's 100ms (max_latency_increase 20%)") {
		t.Errorf("failures = %q", got)
	}
}

func TestDetectable(t *testing.T) {
	cases := func(k, n int) []stats.Tally {
		out := make([]stats.Tally, k)
		for i := range out {
			out[i] = stats.Tally{Passed: n, Total: n}
		}
		return out
	}
	for _, tt := range []struct {
		k, n int
		ok   bool
		need int
	}{
		{5, 5, true, 0},    // 5 × 0.004 = 0.02
		{21, 5, false, 6},  // 21 × 0.004 = 0.083; 21 × 0.0011 = 0.023
		{21, 10, true, 0},  // 21 × 0.0000054
		{200, 3, false, 8}, // 200/C(14,7) = 0.058, 200/C(16,8) = 0.016
	} {
		ok, need := detectable(cases(tt.k, tt.n), cases(tt.k, tt.n))
		if ok != tt.ok || (!ok && need != tt.need) {
			t.Errorf("%d cases × %d samples: detectable = %v, need %d; want %v, %d", tt.k, tt.n, ok, need, tt.ok, tt.need)
		}
	}
}
