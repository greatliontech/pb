package main

import (
	"context"
	"os"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/spf13/cobra"
)

func depCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dep", Short: "dependency operations"}

	cmd.AddCommand(&cobra.Command{
		Use:   "init <module path>",
		Short: "declare a module in the current directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ws, dir, err := workingTree()
			if err != nil {
				return err
			}
			return dep.Init(ws, dir, args[0])
		},
	})

	run := func(f func(context.Context, *dep.Session) error) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			return f(c.Context(), s)
		}
	}

	cmd.AddCommand(&cobra.Command{
		Use: "tidy", Short: "reconcile declarations with imports", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, s *dep.Session) error { return dep.Tidy(ctx, s, os.Stdout) }),
	})
	cmd.AddCommand(&cobra.Command{
		Use: "download", Short: "fetch, verify, and pin the build list", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, s *dep.Session) error { return dep.Download(ctx, s, os.Stdout) }),
	})
	cmd.AddCommand(&cobra.Command{
		Use: "graph", Short: "print the requirement graph", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, s *dep.Session) error { return dep.Graph(ctx, s, os.Stdout) }),
	})
	cmd.AddCommand(&cobra.Command{
		Use: "verify", Short: "verify cached artifacts against pins", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, s *dep.Session) error { return dep.Verify(ctx, s, os.Stdout) }),
	})
	cmd.AddCommand(&cobra.Command{
		Use: "update [module path or oci plugin reference...]", Short: "move requirements to the highest discovered release, or re-resolve a plugin",
		RunE: func(c *cobra.Command, args []string) error {
			settings, s, err := openSession()
			if err != nil {
				return err
			}
			// A plugin named for update is re-resolved through the
			// acquirer the generation verb would run it with — the
			// runner the settings select, its platform the one the
			// seam checks — opened only when a plugin is named, so a
			// module update needs no runner.
			plugins := &lazyUpdater{settings: settings, session: s}
			defer plugins.Close() //nolint:errcheck — the verb's own error is the one reported
			return dep.Update(c.Context(), s, os.Stdout, plugins, args...)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "why <module path>...", Short: "explain why modules are needed", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, s, err := openSession()
			if err != nil {
				return err
			}
			return dep.Why(c.Context(), s, os.Stdout, args...)
		},
	})
	return cmd
}
