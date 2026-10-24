package baseline

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/blashkar/evalwright/internal/runner"
)

// The gate's error rates, measured by simulating runs whose true pass rates
// are known: how often it fails a run in which nothing changed (a false
// alarm), and how often it catches a run in which cases really got worse.

// truth are the per-case pass rates of llama3.2:3b on the compare suite, as
// measured with 10 samples: a realistic mix of cases that always pass,
// never pass, and pass some of the time.
var truth = []float64{
	.8, .9, .4, .4, .9, .7, 1, 0, .8, .9, 1,
	1, 1, .9, .5, .9, 0, 0, .1, .2, 1,
}

// gateFails simulates a baseline run with the rates base and a later run
// with the rates now, n samples per case, and reports whether the gate
// fails the later run. Every case counts as changed, as when the system
// prompt is edited: the hardest case for the gate, since all of them are
// tested.
func gateFails(rng *rand.Rand, base, now []float64, n int) bool {
	sample := func(rates []float64, system string) Model {
		m := Model{Model: "m"}
		for i, p := range rates {
			passed := 0
			for range n {
				if rng.Float64() < p {
					passed++
				}
			}
			m.Cases = append(m.Cases, Case{
				Case:        fmt.Sprint(i),
				Passed:      passed,
				Total:       n,
				Fingerprint: runner.Fingerprint{System: system},
			})
		}
		return m
	}
	mr, cases := compareModel(sample(now, "edited"), sample(base, "original"), false)
	if mr.Dropped() {
		return true
	}
	for _, c := range cases {
		if c.Status == Regressed {
			return true
		}
	}
	return false
}

// failRate returns the fraction of trials in which the gate fails.
func failRate(base, now []float64, n int) float64 {
	const trials = 4000
	rng := rand.New(rand.NewPCG(1, 2))
	failed := 0
	for range trials {
		if gateFails(rng, base, now, n) {
			failed++
		}
	}
	return float64(failed) / trials
}

// with returns truth with the rates of the given cases replaced.
func with(changes map[int]float64) []float64 {
	out := append([]float64(nil), truth...)
	for i, p := range changes {
		out[i] = p
	}
	return out
}

func TestGateCalibration(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	broad := make([]float64, len(truth))
	for i, p := range truth {
		broad[i] = max(0, p-.2)
	}
	// min and max bound the fraction of runs the gate fails at 5, 10 and
	// 20 samples per case. The rates printed with -v are in the README.
	scenarios := []struct {
		name     string
		now      []float64
		min, max [3]float64
	}{
		// At most 5% false alarms, the significance level. Fisher's exact
		// test is conservative, so it's well below.
		{"nothing changed", truth, [3]float64{0, 0, 0}, [3]float64{.05, .05, .05}},
		// The Returns demo: at 5 samples no single case can be flagged, and
		// only the overall drop sometimes is; at 10 the cases always are.
		{"3 cases fail every time", with(map[int]float64{0: 0, 5: 0, 20: 0}), [3]float64{0, .95, .95}, [3]float64{.5, 1, 1}},
		{"1 case fails every time", with(map[int]float64{20: 0}), [3]float64{0, .95, .95}, [3]float64{.1, 1, 1}},
		// A moderate drop in one case is hard to catch at any of these
		// sizes; the README says so.
		{"1 case 90% to 50%", with(map[int]float64{1: .5}), [3]float64{0, 0, 0}, [3]float64{.1, .2, .6}},
		{"every case 20 points worse", broad, [3]float64{.8, .95, .95}, [3]float64{1, 1, 1}},
	}
	for _, s := range scenarios {
		for j, n := range []int{5, 10, 20} {
			rate := failRate(truth, s.now, n)
			t.Logf("%-28s %2d samples: gate fails %5.1f%%", s.name, n, 100*rate)
			if rate < s.min[j] || rate > s.max[j] {
				t.Errorf("%s, %d samples: gate fails %.1f%% of runs, want %.0f–%.0f%%", s.name, n, 100*rate, 100*s.min[j], 100*s.max[j])
			}
		}
	}
}
