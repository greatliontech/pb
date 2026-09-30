package main

import (
	"context"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/migrate"
	"github.com/spf13/cobra"
)

// migrateCmd is the migrate verb (migrate.md REQ-migrate-verb): buf's
// configuration at the working directory becomes pb's files beside
// it, the report printed, the tidy ending it; the status is 1 where
// any fact went unmapped (REQ-migrate-report), the report speaking
// for it. The tidy loads the resolution root the migration wrote at
// the working directory as every dep verb loads one (workspace.md);
// a workspace above the directory governs instead, its membership
// then deciding.
func migrateCmd() *cobra.Command {
	var modulePath string
	var deps, plugins []string
	cmd := &cobra.Command{
		Use: "migrate", Short: "write pb's files from the buf configuration at the working directory", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var repl migrate.Replacements
			for _, v := range deps {
				if err := repl.Replace("dep", v); err != nil {
					return err
				}
			}
			for _, v := range plugins {
				if err := repl.Replace("plugin", v); err != nil {
					return err
				}
			}
			ws, dir, client, err := openTree()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return migrate.Run(c.Context(), migrate.Invocation{
				WS: ws, Dir: dir, HostDir: cwd, ModulePath: modulePath, Replacements: repl, Discovery: client,
				PluginTags: pluginTags,
				Tidy: func(ctx context.Context) error {
					session, err := dep.Load(dep.Config{WS: ws, Dir: dir, Client: client})
					if err != nil {
						return err
					}
					return dep.Tidy(ctx, session, os.Stdout)
				},
				Out: os.Stdout,
			})
		},
	}
	cmd.Flags().StringVar(&modulePath, "module", "", "the pb module path of the configuration's directory; the repository's origin where absent")
	cmd.Flags().StringArrayVar(&deps, "dep", nil, "NAME=PATH[@VERSION]: a BSR module name's module path, and the version to declare; repeatable")
	cmd.Flags().StringArrayVar(&plugins, "plugin", nil, "NAME=REFERENCE: a BSR plugin name's plugin reference; repeatable")
	return cmd
}

// pluginTags lists a plugin repository's tags under the ambient
// credential store, as `pb plugin build` publishes to the registry.
func pluginTags(ctx context.Context, repository string) ([]string, error) {
	repo, err := name.NewRepository(repository)
	if err != nil {
		return nil, err
	}
	return remote.List(repo, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
}
