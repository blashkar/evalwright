package stats

import (
	"math"
	"testing"
	"time"
)

func TestWilson(t *testing.T) {
	tests := []struct {
		passed, total int
		lo, hi        float64
	}{
		// Reference values from a separate Python implementation of the formula.
		{4, 5, 0.3755, 0.9638},
		{22, 25, 0.7004, 0.9583},
		{5, 5, 0.5655, 1},
		{0, 5, 0, 0.4345},
		{50, 100, 0.4038, 0.5962},
		{0, 0, 0, 1}, // no data: anything is possible
	}
	for _, tt := range tests {
		lo, hi := Wilson(tt.passed, tt.total)
		if math.Abs(lo-tt.lo) > 0.0005 || math.Abs(hi-tt.hi) > 0.0005 {
			t.Errorf("Wilson(%d, %d) = %.4f–%.4f, want %.4f–%.4f", tt.passed, tt.total, lo, hi, tt.lo, tt.hi)
		}
	}
}

func TestPercentile(t *testing.T) {
	ms := func(ns ...int) []time.Duration {
		out := make([]time.Duration, len(ns))
		for i, n := range ns {
			out[i] = time.Duration(n) * time.Millisecond
		}
		return out
	}
	ds := ms(50, 10, 40, 20, 30) // unsorted on purpose
	tests := []struct {
		p    float64
		want time.Duration
	}{
		{50, 30 * time.Millisecond},
		{95, 50 * time.Millisecond},
		{20, 10 * time.Millisecond},
		{100, 50 * time.Millisecond},
		{1, 10 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := Percentile(ds, tt.p); got != tt.want {
			t.Errorf("Percentile(p%.0f) = %s, want %s", tt.p, got, tt.want)
		}
	}
	if ds[0] != 50*time.Millisecond {
		t.Error("Percentile sorted its input in place")
	}
	if got := Percentile([]time.Duration(nil), 50); got != 0 {
		t.Errorf("Percentile(nil) = %s, want 0", got)
	}
	if got := Percentile([]int{56, 41, 80}, 50); got != 56 {
		t.Errorf("Percentile of ints = %d, want 56", got)
	}
}

func TestClusteredWilson(t *testing.T) {
	tally := func(passed ...int) []Tally {
		out := make([]Tally, len(passed))
		for i, p := range passed {
			out[i] = Tally{p, 5}
		}
		return out
	}
	tests := []struct {
		name   string
		cases  []Tally
		lo, hi float64
	}{
		// Failures spread across cases as if at random: the samples are worth
		// as much as independent ones, so this is Wilson(21, 25).
		{"independent", tally(4, 5, 5, 3, 4), 0.6535, 0.9360},
		// The same 21/25, but nearly all failures in one case (ICC 0.75):
		// worth 6.25 independent samples, so the interval is much wider.
		{"clustered", tally(1, 5, 5, 5, 5), 0.4500, 0.9712},
		// Every case always passes: the samples add nothing, and the
		// interval is Wilson(5, 5) over the cases.
		{"all pass", tally(5, 5, 5, 5, 5), 0.5655, 1},
		// One sample per case: nothing to correlate, plain Wilson.
		{"one sample each", []Tally{{1, 1}, {1, 1}, {1, 1}, {0, 1}}, 0.3006, 0.9544},
		{"no data", nil, 0, 1},
	}
	for _, tt := range tests {
		lo, hi := ClusteredWilson(tt.cases)
		if math.Abs(lo-tt.lo) > 0.0005 || math.Abs(hi-tt.hi) > 0.0005 {
			t.Errorf("%s: ClusteredWilson = %.4f–%.4f, want %.4f–%.4f", tt.name, lo, hi, tt.lo, tt.hi)
		}
	}
}

func TestPairedDiff(t *testing.T) {
	five := func(passed ...int) []Tally {
		out := make([]Tally, len(passed))
		for i, p := range passed {
			out[i] = Tally{p, 5}
		}
		return out
	}
	tests := []struct {
		name         string
		a, b         []Tally
		mean, lo, hi float64
		significant  bool
	}{
		// The real llama3.2 vs qwen3 run: same 21/25 total, opposite strengths.
		// Differences .6, 0, 0, -.4, -.2: sd .374, t(4) = 2.776.
		{"equal totals", five(4, 5, 5, 3, 4), five(1, 5, 5, 5, 5), 0, -0.4646, 0.4646, false},
		// Every case 5/5 vs 4/5: the differences don't vary, but each rate is
		// itself uncertain, so the interval isn't zero-width.
		{"constant difference", five(5, 5, 5, 5, 5), five(4, 4, 4, 4, 4), 0.2, -0.0682, 0.4682, false},
		// Differences 1, .8, 1, 1, .8, 1 vary less (sd .103) than the rates
		// themselves could, so the within-case noise sets the width; t(5) = 2.571.
		{"clearly better", five(5, 5, 5, 5, 5, 5), five(0, 1, 0, 0, 1, 0), 1 - 2.0/30, 0.7264, 1, true},
		// 5/5 and 0/5 have no measured variance, but 5 samples don't pin a
		// rate down exactly, so even identical results get an interval.
		{"identical", five(5, 0, 5), five(5, 0, 5), 0, -0.4646, 0.4646, false},
		{"one case", five(5), five(0), 1, 0.6334, 1, true},
	}
	for _, tt := range tests {
		d := PairedDiff(tt.a, tt.b)
		if math.Abs(d.Mean-tt.mean) > 0.0005 || math.Abs(d.Lo-tt.lo) > 0.0005 || math.Abs(d.Hi-tt.hi) > 0.0005 {
			t.Errorf("%s: PairedDiff = %.4f (%.4f to %.4f), want %.4f (%.4f to %.4f)", tt.name, d.Mean, d.Lo, d.Hi, tt.mean, tt.lo, tt.hi)
		}
		if d.Significant() != tt.significant || d.Cases != len(tt.a) {
			t.Errorf("%s: significant = %v over %d cases, want %v over %d", tt.name, d.Significant(), d.Cases, tt.significant, len(tt.a))
		}
	}
}

func TestTCritical95(t *testing.T) {
	// Reference values from numerically integrating the t distribution.
	for df, want := range map[int]float64{1: 12.7062, 4: 2.7764, 30: 2.0423, 40: 2.0211, 60: 2.0003, 120: 1.9799} {
		if got := TCritical95(df); math.Abs(got-want) > 0.0002 {
			t.Errorf("TCritical95(%d) = %.4f, want %.4f", df, got, want)
		}
	}
}

func TestFisherExact(t *testing.T) {
	// Reference values computed exactly with rational arithmetic.
	tests := []struct {
		a, b           Tally
		twoSided, less float64
	}{
		{Tally{4, 5}, Tally{1, 5}, 0.206349, 0.996032}, // 4/5 vs 1/5 is not significant
		{Tally{5, 5}, Tally{0, 5}, 0.007937, 1},
		{Tally{0, 5}, Tally{5, 5}, 0.007937, 0.003968},
		{Tally{12, 15}, Tally{3, 15}, 0.002814, 0.999927},
		{Tally{3, 5}, Tally{3, 5}, 1, 0.738095},
		{Tally{2, 10}, Tally{7, 10}, 0.069779, 0.034889},
		{Tally{5, 5}, Tally{5, 5}, 1, 1}, // no failures: nothing differs
		{Tally{0, 0}, Tally{3, 5}, 1, 1},
	}
	for _, tt := range tests {
		two, less := FisherExact(tt.a, tt.b)
		if math.Abs(two-tt.twoSided) > 1e-6 || math.Abs(less-tt.less) > 1e-6 {
			t.Errorf("FisherExact(%v, %v) = %.6f, %.6f, want %.6f, %.6f", tt.a, tt.b, two, less, tt.twoSided, tt.less)
		}
	}
}

func TestHolm(t *testing.T) {
	got := Holm([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.03, 0.06, 0.06, 0.02}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("Holm = %v, want %v", got, want)
		}
	}
	if got := Holm([]float64{0.5, 0.9}); got[0] != 1 || got[1] != 1 {
		t.Errorf("Holm caps at 1, got %v", got)
	}
}

func TestPoints(t *testing.T) {
	for x, want := range map[float64]string{0: "0", 0.0004: "0", 0.004: "+0.4", -0.003: "-0.3", 0.19: "+19", -0.464: "-46"} {
		if got := Points(x); got != want {
			t.Errorf("Points(%v) = %q, want %q", x, got, want)
		}
	}
}
