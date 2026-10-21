package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/blashkar/evalwright/internal/provider"
	"github.com/blashkar/evalwright/internal/report"
	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/suite"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

//go:embed example.yaml
var exampleSuite []byte

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "evalwright",
		Short:   "Regression tests for LLM prompts and local models",
		Version: version,
		// We print errors ourselves in main, and a usage dump is noise when a test fails.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newInitCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "run SUITE...",
		Short: "Run suites and exit non-zero if any case fails",
		Example: `  evalwright run evalwright.yaml
  evalwright run evals/*.yaml -o results.json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuites(cmd.Context(), cmd.OutOrStdout(), args, outPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "also write results as JSON to this file")
	return cmd
}

func runSuites(ctx context.Context, w io.Writer, paths []string, outPath string) error {
	r := runner.Runner{Providers: defaultProviders()}

	var reports []*runner.Report
	failed := false
	for i, path := range paths {
		s, err := suite.Load(path)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Fprintln(w)
		}
		rep, runErr := r.Run(ctx, s)
		if rep != nil {
			// Print partial results even if the run was interrupted.
			if err := report.Table(w, rep); err != nil {
				return err
			}
			reports = append(reports, rep)
			failed = failed || !rep.Passed()
		}
		if runErr != nil {
			return runErr
		}
	}

	if outPath != "" {
		if err := writeJSON(outPath, reports); err != nil {
			return err
		}
	}
	if failed {
		return errSuiteFailed
	}
	return nil
}

func defaultProviders() provider.Registry {
	return provider.Registry{
		"mock": &provider.Mock{},
	}
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

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [FILE]",
		Short: "Create an example suite (default: evalwright.yaml)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "evalwright.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			// O_EXCL makes creation fail if the file exists, so we never overwrite a user's suite.
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s already exists; not overwriting it", path)
			}
			if err != nil {
				return err
			}
			if _, err := f.Write(exampleSuite); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Try: evalwright run %s\n", path, path)
			return nil
		},
	}
}
