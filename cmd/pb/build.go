package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/dep"
)

// buildCmd is the build verb (build.md): the output file as the
// working tree names it, and as given for the report.
func buildCmd() *cobra.Command {
	return &cobra.Command{
		Use: "build <file>", Short: "write the workspace's compiled schema as one descriptor set", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			file, err := treePath(args[0])
			if err != nil {
				return err
			}
			return dep.Build(c.Context(), s, file, args[0], os.Stdout)
		},
	}
}
