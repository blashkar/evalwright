// Package stats has the statistics evalwright reports: confidence
// intervals for pass rates, paired comparisons between models, exact tests
// for per-case differences, and percentiles for timings and token counts.
//
// The methods follow common practice for language model evaluations (see
// Miller, "Adding Error Bars to Evals", 2024): a case's samples are not
// independent of each other, so uncertainty is measured across cases, and
// two models that answered the same cases are compared case by case.
package stats

import (
	"cmp"
	"math"
	"slices"
)

// z95 is the z-score for a 95% confidence level.
const z95 = 1.959964

// Tally counts the passing samples of one case.
type Tally struct {
	Passed, Total int
}

// Rate returns the fraction of samples that passed, or 0 if there were none.
func (t Tally) Rate() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Passed) / float64(t.Total)
}

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
	return wilson(float64(passed)/float64(total), float64(total))
}

// wilson is Wilson's interval for rate p over n trials. n need not be a
// whole number, so it also works with an effective sample size.
func wilson(p, n float64) (lo, hi float64) {
	z2 := z95 * z95
	denom := 1 + z2/n
	center := (p + z2/(2*n)) / denom
	half := z95 * math.Sqrt(p*(1-p)/n+z2/(4*n*n)) / denom
	return max(0, center-half), min(1, center+half)
}

// ClusteredWilson returns the 95% interval for the overall pass rate of
// cases, each with several samples.
//
// Wilson over all samples would treat them as independent, but samples of
// one case are not: a model that gets a case wrong tends to get it wrong
// every time. Five near-identical answers are closer to one observation
// than to five. ClusteredWilson measures how alike the samples within a
// case are (the intraclass correlation), shrinks the sample count to the
// number of independent samples it is worth (Kish's effective sample size),
// and computes Wilson's interval for that. If samples within a case vary as
// much as samples across cases, the result is the plain Wilson interval; if
// every case always gives the same result, it is Wilson's interval over the
// number of cases.
func ClusteredWilson(cases []Tally) (lo, hi float64) {
	var passed, total int
	for _, c := range cases {
		passed += c.Passed
		total += c.Total
	}
	if total == 0 {
		return 0, 1
	}
	n := float64(total)
	return wilson(float64(passed)/n, n/designEffect(cases))
}

// designEffect returns how many times wider sampling within cases makes the
// variance than independent samples would: 1 + (m-1)·ICC for an average of
// m samples per case, with the intraclass correlation (ICC) estimated by
// one-way analysis of variance.
func designEffect(cases []Tally) float64 {
	var k, total, passed, sumSquares float64
	for _, c := range cases {
		if c.Total == 0 {
			continue
		}
		k++
		total += float64(c.Total)
		passed += float64(c.Passed)
		sumSquares += float64(c.Total) * float64(c.Total)
	}
	m := total / k // average samples per case
	if m <= 1 {
		return 1 // one sample per case: nothing to correlate
	}

	icc := 1.0 // the cautious answer when the data can't say
	if k > 1 {
		p := passed / total
		var between, within float64
		for _, c := range cases {
			if c.Total == 0 {
				continue
			}
			n, pc := float64(c.Total), c.Rate()
			between += n * (pc - p) * (pc - p)
			within += n * pc * (1 - pc)
		}
		msb := between / (k - 1)
		msw := within / (total - k)
		m0 := (total - sumSquares/total) / (k - 1)
		if d := msb + (m0-1)*msw; d > 0 {
			icc = min(max((msb-msw)/d, 0), 1)
		}
	}
	return 1 + (m-1)*icc
}

// Diff is the difference between two models' pass rates over the same
// cases, as a fraction between -1 and 1, with its 95% interval.
type Diff struct {
	Mean, Lo, Hi float64
	// Cases is the number of cases the difference was measured on.
	Cases int
}

// Significant reports whether the interval excludes zero, i.e. the run
// shows a real difference rather than noise.
func (d Diff) Significant() bool {
	return d.Lo > 0 || d.Hi < 0
}

// PairedDiff compares a with b case by case: a[i] and b[i] must be the same
// case. Mean is the average of the per-case differences in pass rate.
//
// Pairing removes the variation that comes from some cases being harder than
// others, since both models face the same cases, so it detects a real
// difference with fewer cases than comparing the two overall rates would.
// The interval uses Student's t distribution over the cases. It is never
// narrower than the sampling noise within the cases, so a run in which every
// difference happens to be the same doesn't report false certainty.
func PairedDiff(a, b []Tally) Diff {
	k := min(len(a), len(b))
	if k == 0 {
		return Diff{Lo: -1, Hi: 1}
	}
	d := make([]float64, k)
	var mean, within float64
	for i := range k {
		pa, pb := a[i].Rate(), b[i].Rate()
		d[i] = pa - pb
		mean += d[i]
		within += sampleVariance(pa, a[i].Total) + sampleVariance(pb, b[i].Total)
	}
	n := float64(k)
	mean /= n
	se := math.Sqrt(within) / n

	crit := z95
	if k > 1 {
		var ss float64
		for _, x := range d {
			ss += (x - mean) * (x - mean)
		}
		se = max(se, math.Sqrt(ss/(n-1)/n))
		crit = TCritical95(k - 1)
	}
	return Diff{
		Mean:  mean,
		Lo:    max(-1, mean-crit*se),
		Hi:    min(1, mean+crit*se),
		Cases: k,
	}
}

// sampleVariance is the variance of a pass rate p measured over n samples.
func sampleVariance(p float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return p * (1 - p) / float64(n)
}

// tTable holds the two-sided 95% critical values of Student's t
// distribution for 1 to 30 degrees of freedom.
var tTable = [...]float64{
	12.7062, 4.3027, 3.1824, 2.7764, 2.5706, 2.4469, 2.3646, 2.3060, 2.2622, 2.2281,
	2.2010, 2.1788, 2.1604, 2.1448, 2.1314, 2.1199, 2.1098, 2.1009, 2.0930, 2.0860,
	2.0796, 2.0739, 2.0687, 2.0639, 2.0595, 2.0555, 2.0518, 2.0484, 2.0452, 2.0423,
}

// TCritical95 returns the two-sided 95% critical value of Student's t
// distribution with df degrees of freedom: how many standard errors either
// side of the mean a 95% interval spans when the standard error was itself
// estimated from df+1 values. It is 12.7 for 1 degree of freedom and
// approaches 1.96 as df grows.
func TCritical95(df int) float64 {
	if df < 1 {
		return math.Inf(1)
	}
	if df <= len(tTable) {
		return tTable[df-1]
	}
	// Cornish–Fisher expansion around the normal quantile; accurate to about
	// 1e-4 beyond 30 degrees of freedom.
	z, v := z95, float64(df)
	z3, z5, z7 := z*z*z, z*z*z*z*z, z*z*z*z*z*z*z
	return z + (z3+z)/(4*v) + (5*z5+16*z3+3*z)/(96*v*v) + (3*z7+19*z5+17*z3-15*z)/(384*v*v*v)
}

// FisherExact compares two pass counts with Fisher's exact test. It returns
// the two-sided p-value (how likely counts at least this different would be
// if both rates were the same) and the one-sided p-value for a passing less
// often than b. It is exact, so it is valid for the small sample counts of a
// single case, where approximate tests are not.
func FisherExact(a, b Tally) (twoSided, aLess float64) {
	n := a.Total + b.Total
	passed := a.Passed + b.Passed
	if a.Total == 0 || b.Total == 0 || passed == 0 || passed == n {
		return 1, 1 // nothing to compare: one side is empty, or no row varies
	}
	// With the totals fixed, a's pass count follows a hypergeometric distribution.
	lo, hi := max(0, passed-b.Total), min(passed, a.Total)
	prob := func(x int) float64 {
		return math.Exp(logChoose(a.Total, x) + logChoose(b.Total, passed-x) - logChoose(n, passed))
	}
	observed := prob(a.Passed)
	for x := lo; x <= hi; x++ {
		px := prob(x)
		if px <= observed*(1+1e-7) {
			twoSided += px
		}
		if x <= a.Passed {
			aLess += px
		}
	}
	return min(twoSided, 1), min(aLess, 1)
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// Holm adjusts p-values for testing many cases at once, with the
// Holm–Bonferroni method. At a 5% significance level, each test of 20 cases
// would flag a difference that isn't there one time in twenty; comparing the
// adjusted p-values with 5% keeps the chance of any false flag across all
// cases at 5%.
func Holm(ps []float64) []float64 {
	order := make([]int, len(ps))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int { return cmp.Compare(ps[i], ps[j]) })

	adjusted := make([]float64, len(ps))
	running := 0.0
	for rank, i := range order {
		running = max(running, min(1, float64(len(ps)-rank)*ps[i]))
		adjusted[i] = running
	}
	return adjusted
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
