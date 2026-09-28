package main

import (
	"context"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/userconfig"
)

// cleanCmd is the stores' verb (dep-verbs.md REQ-dep-clean): the
// module cache and the plugin store with its evidence, emptied on
// the machine, no resolution root needed.
func cleanCmd() *cobra.Command {
	var modules, plugins bool
	cmd := &cobra.Command{
		Use: "clean", Short: "empty the module cache and the plugin store", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			settings, err := userconfig.Load()
			if err != nil {
				return err
			}
			stores, err := assembleStores(settings)
			if err != nil {
				return err
			}
			return dep.Clean(c.Context(), stores, c.OutOrStdout(), modules, plugins)
		},
	}
	cmd.Flags().BoolVar(&modules, "modules", false, "empty the module cache alone")
	cmd.Flags().BoolVar(&plugins, "plugins", false, "empty the plugin store and its kept evidence alone")
	return cmd
}

// assembleStores locates what clean empties as the verbs locate them:
// the module cache from the cache setting (the module cache term,
// dep-verbs.md) with the vcs store beneath it, and the plugin store
// and its evidence under the user cache directory.
func assembleStores(settings *userconfig.Settings) (dep.Stores, error) {
	cache, err := moduleCacheDir(settings)
	if err != nil {
		return dep.Stores{}, err
	}
	cacheDir := cache.Value
	workDir, evidenceDir, err := pluginStoreDirs()
	if err != nil {
		return dep.Stores{}, err
	}
	return dep.Stores{
		ModuleCache: osfs.New(cacheDir),
		VCS:         osfs.New(filepath.Join(cacheDir, direct.StoreDir)),
		Plugins: func(ctx context.Context) ([]string, error) {
			return oci.Empty(ctx, workDir, evidenceDir)
		},
	}, nil
}
