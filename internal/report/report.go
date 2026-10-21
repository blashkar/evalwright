// Package report renders run results for people and for other tools.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/blashkar/evalwright/internal/runner"
)

// maxOutputExcerpt limits how much model output is shown for a failure.
const maxOutputExcerpt = 200

// Table writes a results table, the details of every failure, and a per-model summary.
func Table(w io.Writer, rep *runner.Report) error {
	fmt.Fprintf(w, "Suite: %s\n\n", rep.Suite)

	if len(rep.Results) == 0 {
		fmt.Fprintln(w, "No results.")
		_, err := fmt.Fprintf(w, "\n%s  %s in %s\n", verdict(rep), rep.Suite, rep.Duration.Round(time.Millisecond))
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tMODEL\tSAMPLE\tRESULT\tLATENCY")
	for _, r := range rep.Results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", r.Case, r.Model, r.Sample, status(r), r.Latency.Round(time.Millisecond))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	writeFailures(w, rep)

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tPASSED\tPASS RATE\tMEAN LATENCY")
	for _, m := range rep.Summary() {
		fmt.Fprintf(tw, "%s\t%d/%d\t%.0f%%\t%s\n", m.Model, m.Passed, m.Total, m.PassRate()*100, m.MeanLatency.Round(time.Millisecond))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	_, err := fmt.Fprintf(w, "\n%s  %s in %s\n", verdict(rep), rep.Suite, rep.Duration.Round(time.Millisecond))
	return err
}

func verdict(rep *runner.Report) string {
	switch {
	case rep.Interrupted:
		return "INTERRUPTED"
	case rep.Passed():
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
