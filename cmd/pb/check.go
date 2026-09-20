package main

import (
	"os"

	"github.com/go-git/go-git/v6"
	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/check/breaking"
	"github.com/greatliontech/pb/internal/dep"
)

// lintCmd is the lint verb (check-rules.md REQ-check-lint-verb): the
// findings on standard output, the failing status through
// dep.ErrFindings.
func lintCmd() *cobra.Command {
	return &cobra.Command{
		Use: "lint", Short: "evaluate the enabled lint rules over the workspace's protobuf files", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			return dep.Lint(c.Context(), s, os.Stdout, os.Stderr)
		},
	}
}

// breakingCmd is the breaking verb (REQ-check-breaking-verb): a
// reference base is read from the repository the workspace root lies
// in.
func breakingCmd() *cobra.Command {
	return &cobra.Command{
		Use: "breaking", Short: "evaluate the enabled breaking rules against each module's base", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			deps := dep.BreakingDeps{Repo: func() (*git.Repository, string, error) {
				return breaking.RepoOf(rootOSPath(s))
			}}
			return dep.Breaking(c.Context(), s, deps, os.Stdout, os.Stderr)
		},
	}
}
