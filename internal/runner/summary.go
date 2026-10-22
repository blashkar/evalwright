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
}

// PassRate returns the fraction of results that passed, between 0 and 1.
func (m ModelSummary) PassRate() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Passed) / float64(m.Total)
}

// Interval returns the 95% confidence interval for the pass rate: the
// range the model's true pass rate is likely in, given how few samples
// the run had.
func (m ModelSummary) Interval() (lo, hi float64) {
	return stats.Wilson(m.Passed, m.Total)
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
