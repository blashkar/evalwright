package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/blashkar/evalwright/internal/ollama"
	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/report"
	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/suite"
)

type runOptions struct {
	outPath  string
	parallel int
	timeout  time.Duration
	pull     bool
	quiet    bool
	models   []string
}

func newRunCmd() *cobra.Command {
	var opts runOptions
	cmd := &cobra.Command{
		Use:   "run SUITE...",
		Short: "Run suites and exit non-zero if any case fails",
		Example: `  evalwright run evalwright.yaml
  evalwright run evals/*.yaml --pull -o results.json
  evalwright run suite.yaml --parallel 4   # if OLLAMA_NUM_PARALLEL=4
  evalwright run suite.yaml --models ollama:llama3.2:3b,ollama:qwen2.5:7b`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("models") && len(opts.models) == 0 {
				// Otherwise --models "" would quietly run the suite's models.
				return errors.New("--models is empty")
			}
			return runSuites(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.outPath, "output", "o", "", "also write results as JSON to this file")
	f.IntVarP(&opts.parallel, "parallel", "p", 1, "requests to run at once per model; match OLLAMA_NUM_PARALLEL")
	f.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "time limit for each model request and model load (0 for none)")
	f.BoolVar(&opts.pull, "pull", false, "download models that aren't installed")
	f.BoolVarP(&opts.quiet, "quiet", "q", false, "don't print progress while running")
	f.StringSliceVarP(&opts.models, "models", "m", nil,
		"run these models instead of the suite's, comma-separated; the first is the one the others are compared with")
	return cmd
}

// runSuites writes reports to out and progress to errOut, so the final
// table can be piped or redirected without the progress lines.
func runSuites(ctx context.Context, out, errOut io.Writer, paths []string, opts runOptions) error {
	if opts.parallel < 1 {
		return fmt.Errorf("--parallel must be at least 1, got %d", opts.parallel)
	}
	models, err := cleanModels(opts.models)
	if err != nil {
		return err
	}
	progress := errOut
	if opts.quiet {
		progress = io.Discard
	}

	providers, err := newProviders(opts.pull, progress)
	if err != nil {
		return err
	}
	r := runner.Runner{
		Providers:   providers,
		Parallel:    opts.parallel,
		CaseTimeout: opts.timeout,
		OnModel: func(model string) {
			fmt.Fprintf(progress, "▶ %s\n", model)
		},
		OnResult: func(res runner.Result) {
			mark := "✓"
			if !res.Pass {
				mark = "✗"
			}
			fmt.Fprintf(progress, "  %s %s #%d (%s)\n", mark, res.Case, res.Sample, res.Latency.Round(10*time.Millisecond))
		},
	}

	// Load and check every suite before running any, so a mistake in the
	// last suite is reported in seconds rather than after the others have run.
	suites := make([]*suite.Suite, 0, len(paths))
	var loadErrs []error
	for _, path := range paths {
		s, err := suite.Load(path)
		if err != nil {
			loadErrs = append(loadErrs, err)
			continue
		}
		if len(models) > 0 {
			s.Models = models
		}
		if err := r.Check(s); err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		suites = append(suites, s)
		// Warnings point at likely mistakes, so they are shown even with --quiet.
		for _, w := range s.Warnings() {
			fmt.Fprintf(errOut, "warning: %s: %s\n", path, w)
		}
	}
	if err := errors.Join(loadErrs...); err != nil {
		return err
	}

	var reports []*runner.Report
	failed := false
	var runErr error
	for i, s := range suites {
		if i > 0 {
			fmt.Fprintln(out)
		}
		var rep *runner.Report
		rep, runErr = r.Run(ctx, s)
		if rep != nil {
			// Print partial results even if the run was interrupted.
			fmt.Fprintln(progress)
			if err := report.Table(out, rep); err != nil {
				return err
			}
			reports = append(reports, rep)
			failed = failed || !rep.Passed()
		}
		if runErr != nil {
			break
		}
	}

	// Save results even after Ctrl-C: a partial run can take a long time to repeat.
	if opts.outPath != "" && len(reports) > 0 {
		if err := writeJSON(opts.outPath, reports); err != nil {
			return errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		return runErr
	}
	if failed {
		return errSuiteFailed
	}
	return nil
}

// cleanModels trims spaces from a --models list, so "a, b" works, and
// rejects an empty or repeated entry, the same mistakes a suite's model list
// is checked for.
func cleanModels(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		m = strings.TrimSpace(m)
		switch {
		case m == "":
			return nil, errors.New("--models has an empty entry")
		case seen[m]:
			return nil, fmt.Errorf("--models lists %q twice", m)
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

// newProviders returns every provider a suite can use. Creating them makes no
// network calls; the Ollama server is only contacted if a suite uses it.
func newProviders(pull bool, log io.Writer) (provider.Registry, error) {
	client, err := ollama.FromEnv()
	if err != nil {
		return nil, err
	}
	return provider.Registry{
		"mock":   &provider.Mock{},
		"ollama": &provider.Ollama{Client: client, AutoPull: pull, Log: log},
	}, nil
}

func writeJSON(path string, reports []*runner.Report) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// Close can report a failed write (e.g. disk full), so don't drop its error.
	defer func() { err = errors.Join(err, f.Close()) }()
	return report.JSON(f, reports)
}
