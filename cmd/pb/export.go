package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/dep"
)

// exportCmd is the export verb (export.md): the output directory as
// the working tree names it, and as given for the report.
func exportCmd() *cobra.Command {
	var all bool
	var exclude []string
	cmd := &cobra.Command{
		Use: "export <dir>", Short: "materialize the workspace's protobuf sources as one include tree", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			dir, err := treePath(args[0])
			if err != nil {
				return err
			}
			return dep.Export(c.Context(), s, dir, args[0], dep.ExportOptions{All: all, Exclude: exclude}, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every protobuf file of every module of the build, not the import closure alone")
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil, "MODULE: write none of this build-list module's files, the consumer supplying it at the pinned version; repeatable")
	return cmd
}

// treePath is an output path as the working tree names it: absolute,
// rooted as workingTree roots the tree; symbolic links on the way are
// the verb's own to resolve and judge (REQ-export-output,
// REQ-build-output).
func treePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(filepath.ToSlash(abs), "/"), nil
}
