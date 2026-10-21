package runner

import "time"

// ModelSummary aggregates one model's results across all cases and samples.
type ModelSummary struct {
	Model       string
	Passed      int
	Total       int
	MeanLatency time.Duration
}

// PassRate returns the fraction of results that passed, between 0 and 1.
func (m ModelSummary) PassRate() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Passed) / float64(m.Total)
}

// Summary returns per-model totals in the order the models appear in the suite.
func (r *Report) Summary() []ModelSummary {
	var out []ModelSummary
	index := map[string]int{}
	latency := map[string]time.Duration{}

	for _, res := range r.Results {
		i, ok := index[res.Model]
		if !ok {
			i = len(out)
			index[res.Model] = i
			out = append(out, ModelSummary{Model: res.Model})
		}
		out[i].Total++
		if res.Pass {
			out[i].Passed++
		}
		latency[res.Model] += res.Latency
	}
	for i := range out {
		out[i].MeanLatency = latency[out[i].Model] / time.Duration(out[i].Total)
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
