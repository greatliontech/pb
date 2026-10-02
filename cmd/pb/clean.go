package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/spf13/cobra"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/plugin/oci"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/userconfig"
)

// cleanCmd is the stores' verb (dep-verbs.md REQ-dep-clean): the
// module cache, the plugin store with its evidence and the dependency
// source store, emptied on the machine, no resolution root needed.
func cleanCmd() *cobra.Command {
	var modules, plugins, sources bool
	cmd := &cobra.Command{
		Use: "clean", Short: "empty the module cache, the plugin store and the dependency source store", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			settings, err := userconfig.Load()
			if err != nil {
				return err
			}
			stores, err := assembleStores(settings)
			if err != nil {
				return err
			}
			return dep.Clean(c.Context(), stores, c.OutOrStdout(), modules, plugins, sources)
		},
	}
	cmd.Flags().BoolVar(&modules, "modules", false, "empty the module cache alone")
	cmd.Flags().BoolVar(&plugins, "plugins", false, "empty the plugin store and its kept evidence alone")
	cmd.Flags().BoolVar(&sources, "sources", false, "empty the dependency source store the language server fills alone")
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
	sourcesDir, err := sourcesStoreDir()
	if err != nil {
		return dep.Stores{}, err
	}
	return dep.Stores{
		ModuleCache: osfs.New(cacheDir),
		VCS:         osfs.New(filepath.Join(cacheDir, direct.StoreDir)),
		Plugins: func(ctx context.Context) ([]string, error) {
			return oci.Empty(ctx, workDir, evidenceDir)
		},
		Sources: osfs.New(sourcesDir),
	}, nil
}

// sourcesStoreDir is the dependency source store's directory: pb/sources
// under the platform user cache directory (lsp.md
// REQ-lsp-dependency-files), beside the plugin store.
func sourcesStoreDir() (string, error) {
	return userCacheSubdir("the dependency source store", "sources")
}

// userCacheSubdir is pb's directory of the given name under the
// platform user cache directory, the refusal naming what it was for.
func userCacheSubdir(purpose, name string) (string, error) {
	base, err := userconfig.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving the user cache directory for %s (set XDG_CACHE_HOME or HOME): %w", purpose, err)
	}
	return filepath.Join(base, "pb", name), nil
}
