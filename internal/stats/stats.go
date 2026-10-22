// Package stats has the small amount of statistics evalwright reports:
// confidence intervals for pass rates, and percentiles for timings and
// token counts.
package stats

import (
	"cmp"
	"math"
	"slices"
)

// z95 is the z-score for a 95% confidence level.
const z95 = 1.959964

// Wilson returns the 95% Wilson score interval for passed successes out of
// total trials, as fractions between 0 and 1.
//
// LLM output varies, so a pass rate from a handful of samples is only an
// estimate. The interval says how far off it could be: 4 passes out of 5 is
// "80%", but the true rate could plausibly be anywhere from 38% to 96%.
// Wilson's interval is used because, unlike the textbook p ± 1.96·√(p(1-p)/n),
// it stays inside [0, 1] and is sensible for small samples and for rates of
// 0% or 100%.
func Wilson(passed, total int) (lo, hi float64) {
	if total <= 0 {
		return 0, 1
	}
	n := float64(total)
	p := float64(passed) / n
	z2 := z95 * z95
	denom := 1 + z2/n
	center := (p + z2/(2*n)) / denom
	half := z95 * math.Sqrt(p*(1-p)/n+z2/(4*n*n)) / denom
	return max(0, center-half), min(1, center+half)
}

// Percentile returns the p-th percentile (0 < p <= 100) of xs using the
// nearest-rank method, so the result is always one of the measured values.
// It returns the zero value if xs is empty. xs is not modified.
func Percentile[T cmp.Ordered](xs []T, p float64) T {
	if len(xs) == 0 {
		var zero T
		return zero
	}
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}
