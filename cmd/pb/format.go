package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/dep"
)

// formatCmd is the format verb (format.md REQ-format-verb): the
// unformatted files' paths or diffs on standard output, the files
// rewritten under --write, the failing status under --exit-code
// through dep.ErrUnformatted.
func formatCmd() *cobra.Command {
	var opts dep.FormatOptions
	c := &cobra.Command{
		Use: "format", Short: "rewrite the workspace's protobuf files in the canonical form", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			return dep.Format(c.Context(), s, opts, os.Stdout)
		},
	}
	c.Flags().BoolVarP(&opts.Diff, "diff", "d", false, "print a unified diff per file not in canonical form, instead of its path")
	c.Flags().BoolVarP(&opts.Write, "write", "w", false, "rewrite each file not in canonical form in place")
	c.Flags().BoolVar(&opts.ExitCode, "exit-code", false, "exit 1 where any file was not in canonical form")
	return c
}
