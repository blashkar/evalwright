// Package baseline saves a run's results and compares later runs with them,
// so a change that makes a model's answers worse fails the run, while
// failures that were already there don't.
//
// A baseline stores, for each model and case, the pass count, the seeds,
// a fingerprint of what the case sent and checked, and a hash of the
// answers. It stores hashes rather than prompts and answers, so it is small
// and can be committed next to the suite without copying its contents.
package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"time"

	"github.com/blashkar/evalwright/internal/runner"
)

// formatVersion changes when the file format does, so an old evalwright
// doesn't misread a newer baseline.
const formatVersion = 1

// File is a saved baseline: the results of one run of one or more suites.
type File struct {
	Version int     `json:"version"`
	Suites  []Suite `json:"suites"`
}

// Suite is one suite's results.
type Suite struct {
	Name   string  `json:"name"`
	Models []Model `json:"models"`
}

// Model is one model's results on a suite.
type Model struct {
	Model string `json:"model"`
	// Digest identifies the exact build of the model, if the provider
	// reports it.
	Digest          string        `json:"digest,omitempty"`
	LatencyP50      time.Duration `json:"latency_p50_ns,omitempty"`
	TokensPerSecond float64       `json:"tokens_per_second,omitempty"`
	Cases           []Case        `json:"cases"`
}

// Case is one model's results on one case.
type Case struct {
	Case        string             `json:"case"`
	Passed      int                `json:"passed"`
	Total       int                `json:"total"`
	Seeds       []int64            `json:"seeds,omitempty"`
	Fingerprint runner.Fingerprint `json:"fingerprint"`
	// Outputs is a hash of every sample's answer (or error), in sample order.
	Outputs string `json:"outputs"`
}

// FromReports returns a baseline of reps. A baseline must be complete, so an
// interrupted run is an error.
func FromReports(reps []*runner.Report) (*File, error) {
	f := &File{Version: formatVersion}
	for _, rep := range reps {
		if rep.Interrupted {
			return nil, fmt.Errorf("suite %q was interrupted; a baseline needs a complete run", rep.Suite)
		}
		f.Suites = append(f.Suites, fromReport(rep))
	}
	return f, nil
}

// Suite returns the results saved for the suite called name.
func (f *File) Suite(name string) (*Suite, bool) {
	for i := range f.Suites {
		if f.Suites[i].Name == name {
			return &f.Suites[i], true
		}
	}
	return nil, false
}

// Save writes f to path as indented JSON.
func (f *File) Save(path string) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Load reads a baseline saved with Save.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: not a baseline file: %w", path, err)
	}
	switch {
	case f.Version == 0:
		return nil, fmt.Errorf("%s: not a baseline file (no version)", path)
	case f.Version > formatVersion:
		return nil, fmt.Errorf("%s: baseline format %d is newer than this evalwright supports (%d); upgrade evalwright", path, f.Version, formatVersion)
	}
	if len(f.Suites) == 0 {
		return nil, errors.New(path + ": baseline has no suites")
	}
	return &f, nil
}

// fromReport collects rep's results per model and case, in suite order.
func fromReport(rep *runner.Report) Suite {
	fingerprints := map[string]runner.Fingerprint{}
	for _, c := range rep.Cases {
		fingerprints[c.Name] = c.Fingerprint
	}
	digests := map[string]string{}
	for _, m := range rep.Models {
		digests[m.Model] = m.Digest
	}

	type key struct{ model, c string }
	outputs := map[key]hash.Hash{}
	seeds := map[key][]int64{}
	for _, r := range rep.Results {
		k := key{r.Model, r.Case}
		h, ok := outputs[k]
		if !ok {
			h = sha256.New()
			outputs[k] = h
		}
		// Results are in sample order, so equal hashes mean equal answers
		// sample for sample.
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00", r.Sample, r.Output, r.Error)
		if r.Seed != nil {
			seeds[k] = append(seeds[k], *r.Seed)
		}
	}

	s := Suite{Name: rep.Suite}
	cases := rep.CaseSummaries()
	for _, ms := range rep.Summary() {
		m := Model{
			Model:           ms.Model,
			Digest:          digests[ms.Model],
			LatencyP50:      ms.LatencyP50,
			TokensPerSecond: ms.TokensPerSecond,
		}
		for _, c := range cases {
			t, ok := c.Tally(ms.Model)
			if !ok {
				continue
			}
			k := key{ms.Model, c.Case}
			m.Cases = append(m.Cases, Case{
				Case:        c.Case,
				Passed:      t.Passed,
				Total:       t.Total,
				Seeds:       seeds[k],
				Fingerprint: fingerprints[c.Case],
				Outputs:     hex.EncodeToString(outputs[k].Sum(nil)[:8]),
			})
		}
		s.Models = append(s.Models, m)
	}
	return s
}
