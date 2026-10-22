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
