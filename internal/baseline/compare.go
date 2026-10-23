package baseline

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/stats"
	"github.com/blashkar/evalwright/internal/suite"
)

// Status is how a case's results compare with the baseline.
type Status int

const (
	// Identical means the same inputs, checks and model build gave the same
	// answers: nothing changed.
	Identical Status = iota
	// Unchanged means the pass count is the same, though something differs.
	Unchanged
	// Changed means the pass count differs, but not by more than chance
	// would explain.
	Changed
	// Regressed means the case passes significantly less often.
	Regressed
	// Improved means the case passes significantly more often.
	Improved
	// New means the baseline has no results for the case.
	New
	// Removed means the baseline has results this run doesn't.
	Removed
)

func (s Status) String() string {
	return [...]string{"identical", "unchanged", "changed", "regressed", "improved", "new", "removed"}[s]
}

// CaseResult compares one model's results on one case with the baseline.
type CaseResult struct {
	Model, Case string
	Status      Status
	// Now and Base are the pass counts in this run and in the baseline.
	Now, Base stats.Tally
	// P is the Holm-adjusted one-sided p-value for Regressed and Improved:
	// the chance of a change this large if the pass rate hadn't changed.
	P float64
	// Changes names what differs from the baseline, e.g. "prompt" or
	// "model build"; it is empty if the case ran exactly as before.
	Changes []string
}

// ModelResult compares one model's results with the baseline.
type ModelResult struct {
	Model string
	// New is set if the baseline has no results for the model.
	New bool
	// Diff is the pass rate now minus the baseline's, averaged over the
	// cases both ran, with its 95% interval.
	Diff stats.Diff
	// Same is set if every case both runs have has the same pass count.
	Same bool
	// Detectable is false if the run is too small for any single case to
	// be flagged as regressed: with few samples and many cases, even a case
	// that went from passing every sample to failing every one isn't
	// significant once corrected for the number of cases. SamplesNeeded is
	// then the number of samples per case that would make it detectable.
	Detectable    bool
	SamplesNeeded int
	// Digest and BaseDigest identify the model builds, if known.
	Digest, BaseDigest string
	// LatencyP50 and BaseLatencyP50 are the median latencies; zero if not
	// measured.
	LatencyP50, BaseLatencyP50 time.Duration
}

// BuildChanged reports whether the model was re-downloaded as a different
// build since the baseline.
func (m ModelResult) BuildChanged() bool {
	return m.Digest != "" && m.BaseDigest != "" && m.Digest != m.BaseDigest
}

// LatencyIncrease returns how much slower the median latency is than the
// baseline's, as a fraction (0.2 is 20% slower), and false if either wasn't
// measured or is under a millisecond, too short to compare.
func (m ModelResult) LatencyIncrease() (float64, bool) {
	if m.LatencyP50 < time.Millisecond || m.BaseLatencyP50 < time.Millisecond {
		return 0, false
	}
	return float64(m.LatencyP50)/float64(m.BaseLatencyP50) - 1, true
}

// Comparison compares one suite's run with its baseline.
type Comparison struct {
	Suite string
	// Missing is set if the baseline has no results for the suite.
	Missing bool
	Models  []ModelResult
	// Cases lists every case of every model, in suite order, followed by
	// the cases only the baseline has.
	Cases []CaseResult
	// RemovedModels are models the baseline has that this run didn't run.
	RemovedModels []string
}

// significance is the p-value below which a change counts as real.
const significance = 0.05

// Compare compares rep with the baseline saved for its suite in base.
func Compare(base *File, rep *runner.Report) *Comparison {
	cmp := &Comparison{Suite: rep.Suite}
	saved, ok := base.Suite(rep.Suite)
	now := fromReport(rep)
	if !ok {
		cmp.Missing = true
		saved = &Suite{}
	}

	for _, m := range now.Models {
		i := slices.IndexFunc(saved.Models, func(b Model) bool { return b.Model == m.Model })
		if i < 0 {
			cmp.Models = append(cmp.Models, ModelResult{Model: m.Model, New: true, Digest: m.Digest, LatencyP50: m.LatencyP50})
			for _, c := range m.Cases {
				cmp.Cases = append(cmp.Cases, CaseResult{Model: m.Model, Case: c.Case, Status: New, Now: tally(c)})
			}
			continue
		}
		mr, cases := compareModel(m, saved.Models[i], rep.Interrupted)
		cmp.Models = append(cmp.Models, mr)
		cmp.Cases = append(cmp.Cases, cases...)
	}
	for _, b := range saved.Models {
		if !slices.ContainsFunc(now.Models, func(m Model) bool { return m.Model == b.Model }) {
			cmp.RemovedModels = append(cmp.RemovedModels, b.Model)
		}
	}
	return cmp
}

// compareModel compares one model's cases with the baseline's. Cases the
// run didn't reach because it was interrupted aren't reported as removed.
func compareModel(now, base Model, interrupted bool) (ModelResult, []CaseResult) {
	mr := ModelResult{
		Model:          now.Model,
		Digest:         now.Digest,
		BaseDigest:     base.Digest,
		LatencyP50:     now.LatencyP50,
		BaseLatencyP50: base.LatencyP50,
	}
	var buildChange []string
	if mr.BuildChanged() {
		buildChange = []string{"model build"}
	}

	var out []CaseResult
	var shared []int // indexes into out of the cases both runs have
	var nowT, baseT []stats.Tally
	var lessP, moreP []float64
	for _, c := range now.Cases {
		i := slices.IndexFunc(base.Cases, func(b Case) bool { return b.Case == c.Case })
		if i < 0 {
			out = append(out, CaseResult{Model: now.Model, Case: c.Case, Status: New, Now: tally(c)})
			continue
		}
		b := base.Cases[i]
		r := CaseResult{Model: now.Model, Case: c.Case, Now: tally(c), Base: tally(b)}
		r.Changes = append(c.Fingerprint.Changes(b.Fingerprint), buildChange...)
		if c.Total != b.Total {
			r.Changes = append(r.Changes, "samples")
		}
		switch {
		case len(r.Changes) == 0 && c.Outputs == b.Outputs:
			r.Status = Identical
		case r.Now == r.Base:
			r.Status = Unchanged
		default:
			r.Status = Changed
		}
		_, less := stats.FisherExact(r.Now, r.Base)
		_, more := stats.FisherExact(r.Base, r.Now)
		shared = append(shared, len(out))
		nowT, baseT = append(nowT, r.Now), append(baseT, r.Base)
		lessP, moreP = append(lessP, less), append(moreP, more)
		out = append(out, r)
	}
	if !interrupted {
		for _, b := range base.Cases {
			if !slices.ContainsFunc(now.Cases, func(c Case) bool { return c.Case == b.Case }) {
				out = append(out, CaseResult{Model: now.Model, Case: b.Case, Status: Removed, Base: tally(b)})
			}
		}
	}

	// Each direction is its own family of tests, corrected for the number
	// of cases, so testing 20 cases doesn't flag a chance difference.
	for j, p := range stats.Holm(lessP) {
		if r := &out[shared[j]]; p < significance && r.Status == Changed {
			r.Status, r.P = Regressed, p
		}
	}
	for j, p := range stats.Holm(moreP) {
		if r := &out[shared[j]]; p < significance && r.Status == Changed {
			r.Status, r.P = Improved, p
		}
	}
	mr.Diff = stats.PairedDiff(nowT, baseT)
	mr.Same = slices.Equal(nowT, baseT)
	mr.Detectable, mr.SamplesNeeded = detectable(nowT, baseT)
	return mr, out
}

// detectable reports whether the strongest possible single-case regression,
// all samples passing before and none after, would be significant across
// these cases, and if not, how many samples per case would make it so.
func detectable(now, base []stats.Tally) (bool, int) {
	if len(now) == 0 {
		return true, 0
	}
	n := math.MaxInt
	for i := range now {
		n = min(n, now[i].Total, base[i].Total)
	}
	worst := func(n int) float64 { // Holm-adjusted p of n/n falling to 0/n
		_, p := stats.FisherExact(stats.Tally{Passed: 0, Total: n}, stats.Tally{Passed: n, Total: n})
		return p * float64(len(now))
	}
	if worst(n) < significance {
		return true, 0
	}
	need := n + 1
	for worst(need) >= significance {
		need++
	}
	return false, need
}

func tally(c Case) stats.Tally {
	return stats.Tally{Passed: c.Passed, Total: c.Total}
}

// Failures returns the reasons rep fails, or nil if it passes. cmp is the
// comparison with the baseline, or nil if there is none.
//
// Without a baseline, every case must pass, or, if the suite sets
// min_pass_rate, every model must reach it. With a baseline, the run fails
// only if it got worse: a case regressed, a model's overall pass rate
// dropped significantly, or it broke the suite's min_pass_rate or
// max_latency_increase. Failures already in the baseline, new models and
// new cases never fail it.
func Failures(rep *runner.Report, s *suite.Suite, cmp *Comparison) []string {
	var out []string
	if rep.Interrupted {
		out = append(out, "the run was interrupted")
	}
	summary := rep.Summary()
	if s.MinPassRate != nil {
		for _, m := range summary {
			if m.PassRate() < float64(*s.MinPassRate) {
				out = append(out, fmt.Sprintf("%s passed %s of samples, below min_pass_rate %s",
					m.Model, percent(m.PassRate()), s.MinPassRate))
			}
		}
	}

	if cmp == nil {
		if s.MinPassRate == nil {
			var failed, total int
			for _, m := range summary {
				failed, total = failed+m.Total-m.Passed, total+m.Total
			}
			if failed > 0 {
				out = append(out, fmt.Sprintf("%d of %d results failed", failed, total))
			}
		}
		return out
	}

	for _, c := range cmp.Cases {
		if c.Status == Regressed {
			out = append(out, fmt.Sprintf("%s regressed on %s: %d/%d, was %d/%d (%s)",
				c.Model, c.Case, c.Now.Passed, c.Now.Total, c.Base.Passed, c.Base.Total, stats.PValue(c.P)))
		}
	}
	for _, m := range cmp.Models {
		if m.New {
			continue
		}
		if m.Diff.Hi < 0 {
			out = append(out, fmt.Sprintf("%s passes less often than in the baseline: %s points (95%% interval %s to %s)",
				m.Model, stats.Points(m.Diff.Mean), stats.Points(m.Diff.Lo), stats.Points(m.Diff.Hi)))
		}
		if limit := s.MaxLatencyIncrease; limit != nil {
			if inc, ok := m.LatencyIncrease(); ok && inc > float64(*limit) {
				out = append(out, fmt.Sprintf("%s median latency is %s, %s slower than the baseline's %s (max_latency_increase %s)",
					m.Model, m.LatencyP50.Round(time.Millisecond), percent(inc), m.BaseLatencyP50.Round(time.Millisecond), limit))
			}
		}
	}
	return out
}

func percent(f float64) string {
	return fmt.Sprintf("%.0f%%", f*100)
}
