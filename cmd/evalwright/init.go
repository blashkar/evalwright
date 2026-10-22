package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/blashkar/evalwright/examples"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [FILE]",
		Short: "Create a starter suite (default: evalwright.yaml)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "evalwright.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			src, err := fs.ReadFile(examples.FS, examples.Quickstart)
			if err != nil {
				return err
			}

			// O_EXCL makes creation fail if the file exists, so we never overwrite a user's suite.
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s already exists; not overwriting it", path)
			}
			if err != nil {
				return err
			}
			if _, err := f.Write(src); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Try: evalwright run %s --pull\n", path, path)
			return nil
		},
	}
}
