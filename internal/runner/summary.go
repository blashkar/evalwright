package runner

import (
	"time"

	"github.com/blashkar/evalwright/internal/stats"
)

// ModelSummary aggregates one model's results across all cases and samples.
type ModelSummary struct {
	Model  string
	Passed int
	Total  int
	// Errors counts requests that got no answer (timeouts, connection errors).
	Errors int

	// The timings below cover only requests that got an answer: counting a
	// failed request's zero latency would make an unreliable model look fast.
	// Each is zero if it wasn't measured.
	LatencyP50 time.Duration
	LatencyP95 time.Duration
	// FirstTokenP50 is the median time until the first token arrived.
	FirstTokenP50 time.Duration
	// OutputTokensP50 is the median answer length in tokens. Latency
	// depends on it as much as on speed: a fast model that writes twice as
	// much can still take longer.
	OutputTokensP50 int
	// TokensPerSecond is the total output tokens divided by the total time
	// spent generating them, so long answers weigh more than short ones.
	TokensPerSecond float64

	// Load is how long the model took to load before its cases ran, if
	// Loaded is set. It is close to zero if the model was already in memory.
	Load   time.Duration
	Loaded bool

	// cases holds the pass counts of each case, in suite order.
	cases []stats.Tally
}

// PassRate returns the fraction of results that passed, between 0 and 1.
func (m ModelSummary) PassRate() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Passed) / float64(m.Total)
}

// Interval returns the 95% confidence interval for the pass rate: the
// range the model's true pass rate is likely in, given how many cases and
// samples the run had. Samples of one case count for less when they tend to
// agree with each other (see stats.ClusteredWilson).
func (m ModelSummary) Interval() (lo, hi float64) {
	return stats.ClusteredWilson(m.cases)
}

// Answered is the number of requests that got an answer.
func (m ModelSummary) Answered() int {
	return m.Total - m.Errors
}

// Summary returns per-model totals in the order the models appear in the suite.
func (r *Report) Summary() []ModelSummary {
	var out []ModelSummary
	index := map[string]int{}
	type timings struct {
		latency, firstToken []time.Duration
		outputTokens        []int
		tokens              int
		generation          time.Duration
	}
	var times []timings

	for _, res := range r.Results {
		i, ok := index[res.Model]
		if !ok {
			i = len(out)
			index[res.Model] = i
			out = append(out, ModelSummary{Model: res.Model})
			times = append(times, timings{})
		}
		out[i].Total++
		if res.Pass {
			out[i].Passed++
		}
		if res.Error != "" {
			out[i].Errors++
			continue
		}
		t := &times[i]
		t.latency = append(t.latency, res.Latency)
		if ft := res.Metrics.TimeToFirstToken; ft > 0 {
			t.firstToken = append(t.firstToken, ft)
		}
		if res.Metrics.OutputTokens > 0 {
			t.outputTokens = append(t.outputTokens, res.Metrics.OutputTokens)
		}
		if res.Metrics.OutputTokens > 0 && res.Metrics.GenerationDuration > 0 {
			t.tokens += res.Metrics.OutputTokens
			t.generation += res.Metrics.GenerationDuration
		}
	}

	for i := range out {
		t := times[i]
		out[i].LatencyP50 = stats.Percentile(t.latency, 50)
		out[i].LatencyP95 = stats.Percentile(t.latency, 95)
		out[i].FirstTokenP50 = stats.Percentile(t.firstToken, 50)
		out[i].OutputTokensP50 = stats.Percentile(t.outputTokens, 50)
		if t.generation > 0 {
			out[i].TokensPerSecond = float64(t.tokens) / t.generation.Seconds()
		}
	}
	for _, c := range r.CaseSummaries() {
		for i := range out {
			if t, ok := c.Tally(out[i].Model); ok {
				out[i].cases = append(out[i].cases, t)
			}
		}
	}
	for _, l := range r.Loads {
		if i, ok := index[l.Model]; ok {
			out[i].Load, out[i].Loaded = l.Time, true
		}
	}
	return out
}

// CaseSummary counts one case's passes for each model.
type CaseSummary struct {
	Case   string
	Passed map[string]int // by model
	Total  map[string]int // by model
}

// Tally returns model's pass count on the case, and false if the model
// didn't run it.
func (c CaseSummary) Tally(model string) (stats.Tally, bool) {
	n, ok := c.Total[model]
	return stats.Tally{Passed: c.Passed[model], Total: n}, ok
}

// CaseSummaries returns per-case, per-model pass counts in the order the
// cases appear in the suite. A model's overall pass rate can hide that it
// fails one kind of case every time; this shows where the failures are.
func (r *Report) CaseSummaries() []CaseSummary {
	var out []CaseSummary
	index := map[string]int{}
	for _, res := range r.Results {
		i, ok := index[res.Case]
		if !ok {
			i = len(out)
			index[res.Case] = i
			out = append(out, CaseSummary{Case: res.Case, Passed: map[string]int{}, Total: map[string]int{}})
		}
		out[i].Total[res.Model]++
		if res.Pass {
			out[i].Passed[res.Model]++
		}
	}
	return out
}

// Comparison sets one model's results against a reference model's, case by
// case. Both models answered the same cases with the same seeds, so the
// comparison is paired.
type Comparison struct {
	Model, Reference string
	// Diff is Model's pass rate minus Reference's, averaged over the cases
	// both ran, with its 95% interval.
	Diff stats.Diff
	// Better and Worse list the cases where Model passed significantly more
	// or less often than Reference. Significance is judged with Fisher's
	// exact test, corrected for the number of cases (Holm), so that with
	// many cases a chance difference isn't reported as a real one.
	Better, Worse []CaseDiff
}

// CaseDiff is a significant difference on one case.
type CaseDiff struct {
	Case             string
	Model, Reference stats.Tally
	// P is the Holm-adjusted p-value: the chance of a difference this large
	// if the two models were equally good at the case.
	P float64
}

// significance is the p-value below which a difference counts as real.
const significance = 0.05

// Compare compares every model with the first model in the suite, which
// serves as the reference: typically the model in use, and the question
// is whether another one would do as well. It returns nil for fewer than
// two models.
func (r *Report) Compare() []Comparison {
	summary := r.Summary()
	if len(summary) < 2 {
		return nil
	}
	ref := summary[0].Model
	cases := r.CaseSummaries()

	var out []Comparison
	for _, m := range summary[1:] {
		comp := Comparison{Model: m.Model, Reference: ref}
		var mine, theirs []stats.Tally
		var names []string
		var ps []float64
		for _, c := range cases {
			a, okA := c.Tally(m.Model)
			b, okB := c.Tally(ref)
			if !okA || !okB {
				continue // not run by both, e.g. interrupted
			}
			mine, theirs, names = append(mine, a), append(theirs, b), append(names, c.Case)
			p, _ := stats.FisherExact(a, b)
			ps = append(ps, p)
		}
		comp.Diff = stats.PairedDiff(mine, theirs)
		for i, p := range stats.Holm(ps) {
			if p >= significance {
				continue
			}
			d := CaseDiff{Case: names[i], Model: mine[i], Reference: theirs[i], P: p}
			if mine[i].Rate() > theirs[i].Rate() {
				comp.Better = append(comp.Better, d)
			} else {
				comp.Worse = append(comp.Worse, d)
			}
		}
		out = append(out, comp)
	}
	return out
}

// Passed reports whether the run finished and every result passed.
// An interrupted run never passes, even if everything that ran did.
func (r *Report) Passed() bool {
	if r.Interrupted {
		return false
	}
	for _, res := range r.Results {
		if !res.Pass {
			return false
		}
	}
	return true
}
