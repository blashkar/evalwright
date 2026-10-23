// Package report renders run results for people and for other tools.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/blashkar/evalwright/internal/baseline"
	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/stats"
)

// maxOutputExcerpt limits how much model output is shown for a failure.
const maxOutputExcerpt = 200

// Gate is what decides whether a run passes: the comparison with a
// baseline, if there is one, and the reasons the run fails.
type Gate struct {
	Baseline *baseline.Comparison
	// Failures are the reasons the run fails; none means it passes.
	Failures []string
}

// Table writes a results table, the details of every failure, pass counts
// per case, per-model quality and speed summaries, and, if gate is set, the
// comparison with the baseline and the reasons the run fails. If gate is
// nil, the run passes only if every result passed.
func Table(w io.Writer, rep *runner.Report, gate *Gate) error {
	fmt.Fprintf(w, "Suite: %s\n\n", rep.Suite)

	if len(rep.Results) == 0 {
		fmt.Fprintln(w, "No results.")
		_, err := fmt.Fprintf(w, "\n%s  %s in %s\n", verdict(rep, gate), rep.Suite, rep.Duration.Round(time.Millisecond))
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tMODEL\tSAMPLE\tRESULT\tLATENCY")
	for _, r := range rep.Results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", r.Case, r.Model, r.Sample, status(r), latency(r))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	writeFailures(w, rep)

	summary := rep.Summary()
	if err := writeCases(w, rep, summary); err != nil {
		return err
	}
	if err := writeQuality(w, summary); err != nil {
		return err
	}
	writeComparison(w, rep.Compare())
	if err := writeSpeed(w, summary); err != nil {
		return err
	}
	if gate != nil {
		if err := writeBaseline(w, gate.Baseline); err != nil {
			return err
		}
		if len(gate.Failures) > 0 {
			fmt.Fprintln(w, "\nFailed because:")
			for _, f := range gate.Failures {
				fmt.Fprintf(w, "  - %s\n", f)
			}
		}
	}

	_, err := fmt.Fprintf(w, "\n%s  %s in %s\n", verdict(rep, gate), rep.Suite, rep.Duration.Round(time.Millisecond))
	return err
}

// writeBaseline says how the run compares with the baseline: each model's
// overall change, and every case that didn't run exactly as before.
func writeBaseline(w io.Writer, cmp *baseline.Comparison) error {
	if cmp == nil {
		return nil
	}
	if cmp.Missing {
		_, err := fmt.Fprintf(w, "\nThe baseline has no results for suite %s, so every case is new.\n", cmp.Suite)
		return err
	}

	fmt.Fprintln(w, "\nCompared with the baseline:")
	for _, m := range cmp.Models {
		d := m.Diff
		switch {
		case m.New:
			fmt.Fprintf(w, "  %s: new, not in the baseline.\n", m.Model)
		case d.Cases == 0:
			fmt.Fprintf(w, "  %s: no cases in common.\n", m.Model)
		case m.Same:
			fmt.Fprintf(w, "  %s: same pass count on every case.\n", m.Model)
		case d.Significant() && d.Mean > 0:
			fmt.Fprintf(w, "  %s passes more often: %s points (95%% interval %s to %s).\n",
				m.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		case d.Significant():
			fmt.Fprintf(w, "  %s passes less often: %s points (95%% interval %s to %s).\n",
				m.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		default:
			fmt.Fprintf(w, "  %s: no significant change (%s points, 95%% interval %s to %s).\n",
				m.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		}
		if !m.New && !m.Detectable {
			fmt.Fprintf(w, "    Too few samples to flag a single case: with %d cases, even a case going from all samples passing to none isn't significant. Use %d or more samples per case (--samples) for the gate to catch one case getting worse.\n",
				m.Diff.Cases, m.SamplesNeeded)
		}
		if m.BuildChanged() {
			fmt.Fprintf(w, "    model build changed: %s, was %s\n", short(m.Digest), short(m.BaseDigest))
		}
		if inc, ok := m.LatencyIncrease(); ok {
			fmt.Fprintf(w, "    median latency %s, was %s (%+.0f%%)\n",
				ms(m.LatencyP50), ms(m.BaseLatencyP50), inc*100)
		}
	}
	for _, m := range cmp.RemovedModels {
		fmt.Fprintf(w, "  %s: in the baseline, not run this time.\n", m)
	}

	identical := 0
	var rows []baseline.CaseResult
	for _, c := range cmp.Cases {
		if c.Status == baseline.Identical {
			identical++
		} else {
			rows = append(rows, c)
		}
	}
	if len(rows) > 0 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  CASE\tMODEL\tRESULT\tNOW\tBASELINE\tWHAT CHANGED")
		for _, c := range rows {
			result := c.Status.String()
			if c.Status == baseline.Regressed || c.Status == baseline.Improved {
				result = fmt.Sprintf("%s (%s)", strings.ToUpper(result), stats.PValue(c.P))
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", c.Case, c.Model, result,
				fraction(c.Now, c.Status != baseline.Removed), fraction(c.Base, c.Status != baseline.New), changes(c))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	switch {
	case identical > 0 && len(rows) == 0:
		fmt.Fprintf(w, "  Every case (%d) gave the same answers as in the baseline.\n", identical)
	case identical > 0:
		fmt.Fprintf(w, "  The other %d gave the same answers as in the baseline.\n", identical)
	}
	return nil
}

func fraction(t stats.Tally, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%d/%d", t.Passed, t.Total)
}

func changes(c baseline.CaseResult) string {
	switch {
	case c.Status == baseline.New || c.Status == baseline.Removed:
		return "-"
	case len(c.Changes) == 0:
		return "answers only" // same inputs, so the model or server answered differently
	default:
		return strings.Join(c.Changes, ", ")
	}
}

// short shortens a digest for display.
func short(digest string) string {
	digest = strings.TrimPrefix(digest, "sha256:")
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// writeCases writes how often each case passed for each model. It is
// skipped when every case ran once against one model, because the results
// table above already says the same thing.
func writeCases(w io.Writer, rep *runner.Report, summary []runner.ModelSummary) error {
	cases := rep.CaseSummaries()
	repeated := false
	for _, c := range cases {
		for _, n := range c.Total {
			repeated = repeated || n > 1
		}
	}
	if len(summary) < 2 && !repeated {
		return nil
	}

	fmt.Fprintln(w, "\nPassed, by case:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "CASE")
	for _, m := range summary {
		fmt.Fprintf(tw, "\t%s", m.Model)
	}
	fmt.Fprintln(tw)
	for _, c := range cases {
		fmt.Fprint(tw, c.Case)
		for _, m := range summary {
			t, ok := c.Tally(m.Model)
			switch {
			case !ok:
				fmt.Fprint(tw, "\t-") // not run, e.g. interrupted
			case repeated:
				// Samples of one case are what this interval is about, so
				// plain Wilson is right here.
				lo, hi := stats.Wilson(t.Passed, t.Total)
				fmt.Fprintf(tw, "\t%d/%d (%s)", t.Passed, t.Total, percentRange(lo, hi))
			default:
				fmt.Fprintf(tw, "\t%d/%d", t.Passed, t.Total)
			}
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func writeQuality(w io.Writer, summary []runner.ModelSummary) error {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tPASSED\tPASS RATE\t95% INTERVAL\tERRORS")
	for _, m := range summary {
		fmt.Fprintf(tw, "%s\t%d/%d\t%.0f%%\t%s\t%d\n",
			m.Model, m.Passed, m.Total, m.PassRate()*100, percentRange(m.Interval()), m.Errors)
	}
	return tw.Flush()
}

// minCases is the fewest cases for which a comparison says something about
// the task in general rather than only about the cases in the suite.
const minCases = 10

// writeComparison says how each model did against the first one, case by
// case, and which differences are real rather than noise.
func writeComparison(w io.Writer, comps []runner.Comparison) {
	if len(comps) == 0 {
		return
	}
	fmt.Fprintf(w, "\nCompared with %s (the first model), case by case:\n", comps[0].Reference)
	for _, c := range comps {
		d := c.Diff
		switch {
		case d.Cases == 0:
			fmt.Fprintf(w, "  %s: no cases in common.\n", c.Model)
			continue
		case d.Significant() && d.Mean > 0:
			fmt.Fprintf(w, "  %s passes more often: %s points (95%% interval %s to %s).\n",
				c.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		case d.Significant():
			fmt.Fprintf(w, "  %s passes less often: %s points (95%% interval %s to %s).\n",
				c.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		case c.Same:
			fmt.Fprintf(w, "  %s: same pass count on every case.\n", c.Model)
		default:
			fmt.Fprintf(w, "  %s: no significant difference (%s points, 95%% interval %s to %s).\n",
				c.Model, stats.Points(d.Mean), stats.Points(d.Lo), stats.Points(d.Hi))
		}
		writeCaseDiffs(w, "better", c.Better)
		writeCaseDiffs(w, "worse", c.Worse)
	}

	if n := comps[0].Diff.Cases; n < minCases {
		fmt.Fprintf(w, "  Only %d cases: this describes these cases, not the task in general. More cases (%d or more), not more samples, make a general comparison possible.\n",
			n, minCases)
	}
}

func writeCaseDiffs(w io.Writer, label string, diffs []runner.CaseDiff) {
	for _, d := range diffs {
		fmt.Fprintf(w, "    %s on %s: %d/%d vs %d/%d (%s)\n", label, d.Case,
			d.Model.Passed, d.Model.Total, d.Reference.Passed, d.Reference.Total, stats.PValue(d.P))
	}
}

func percentRange(lo, hi float64) string {
	return fmt.Sprintf("%.0f–%.0f%%", lo*100, hi*100)
}

func writeSpeed(w io.Writer, summary []runner.ModelSummary) error {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tLATENCY P50\tLATENCY P95\tFIRST TOKEN P50\tANSWER TOKENS P50\tTOKENS/S\tLOAD")
	for _, m := range summary {
		if m.Answered() == 0 {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t%s\n", m.Model, loadTime(m))
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Model,
			ms(m.LatencyP50), ms(m.LatencyP95), orDash(m.FirstTokenP50), count(m.OutputTokensP50),
			tokensPerSecond(m.TokensPerSecond), loadTime(m))
	}
	return tw.Flush()
}

func ms(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// orDash formats d, or "-" if it wasn't measured.
func orDash(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return ms(d)
}

// count formats n, or "-" if it wasn't measured.
func count(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

func tokensPerSecond(tps float64) string {
	if tps == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f", tps)
}

func loadTime(m runner.ModelSummary) string {
	if !m.Loaded {
		return "-"
	}
	return ms(m.Load)
}

func verdict(rep *runner.Report, gate *Gate) string {
	switch {
	case rep.Interrupted:
		return "INTERRUPTED"
	case gate != nil && len(gate.Failures) == 0, gate == nil && rep.Passed():
		return "PASS"
	default:
		return "FAIL"
	}
}

func writeFailures(w io.Writer, rep *runner.Report) {
	header := false
	for _, r := range rep.Results {
		if r.Pass {
			continue
		}
		if !header {
			fmt.Fprintln(w, "\nFailures:")
			header = true
		}
		fmt.Fprintf(w, "\n  ✗ %s · %s · sample %d\n", r.Case, r.Model, r.Sample)
		if r.Error != "" {
			fmt.Fprintf(w, "      error: %s\n", r.Error)
			continue
		}
		for _, c := range r.Checks {
			if !c.Pass {
				fmt.Fprintf(w, "      %s: %s\n", c.Assertion, c.Detail)
			}
		}
		fmt.Fprintf(w, "      output: %q\n", excerpt(r.Output))
	}
}

func status(r runner.Result) string {
	switch {
	case r.Error != "":
		return "ERROR"
	case r.Pass:
		return "pass"
	default:
		return "FAIL"
	}
}

// latency is "-" for a request that got no answer: its zero latency
// would read as an instant reply.
func latency(r runner.Result) string {
	if r.Error != "" {
		return "-"
	}
	return r.Latency.Round(time.Millisecond).String()
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxOutputExcerpt {
		return s
	}
	return string([]rune(s)[:maxOutputExcerpt]) + "…"
}

// JSON writes reports as indented JSON, for saving results and for other tools.
func JSON(w io.Writer, reps []*runner.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reps)
}
