package main

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/blashkar/evalwright/internal/ollama"
)

func newModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List models installed in Ollama, as you'd write them in a suite",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := ollama.FromEnv()
			if err != nil {
				return err
			}
			models, err := client.List(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(models) == 0 {
				fmt.Fprintf(out, "No models installed in Ollama at %s. Try: ollama pull llama3.2:3b\n", client.Host())
				return nil
			}

			slices.SortFunc(models, func(a, b ollama.Model) int { return strings.Compare(a.Name, b.Name) })
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "MODEL\tPARAMS\tQUANT\tSIZE\tMODIFIED")
			for _, m := range models {
				fmt.Fprintf(tw, "ollama:%s\t%s\t%s\t%.1f GB\t%s\n",
					m.Name, m.Details.ParameterSize, m.Details.QuantizationLevel,
					float64(m.Size)/1e9, m.ModifiedAt.Format(time.DateOnly))
			}
			return tw.Flush()
		},
	}
}
