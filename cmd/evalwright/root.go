package main

import (
	"github.com/spf13/cobra"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "evalwright",
		Short:   "Regression tests for LLM prompts and local models",
		Version: version,
		// We print errors ourselves in main, and a usage dump is noise when a test fails.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newInitCmd(), newModelsCmd())
	return root
}
