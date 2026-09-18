// Command pb is the protobuf module manager. This layer is assembly
// only: it reads the environment, wires the seams, and hands every
// observable behavior to the verb layer (internal/dep), where it lives
// under test.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/direct"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
	"github.com/greatliontech/pb/internal/proxy"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pb",
		Short:         "protobuf module manager",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(depCmd())
	root.AddCommand(generateCmd())
	return root
}

// loadSession assembles the resolution session at the working
// directory: the working tree, the fetch-verify client, and the root's
// module files, lockfile, and trust policy.
func loadSession() (*dep.Session, error) {
	ws, dir, err := workingTree()
	if err != nil {
		return nil, err
	}
	client, err := assembleClient()
	if err != nil {
		return nil, err
	}
	return dep.Load(dep.Config{WS: ws, Dir: dir, Client: client})
}

// generateCmd is the generation verb (generation.md REQ-gen-verb): the
// native runner over pb's plugin store, which sits beside the module
// cache rather than inside it — the module cache root holds module
// artifacts only (dep-verbs.md REQ-dep-cache-layout).
func generateCmd() *cobra.Command {
	return &cobra.Command{
		Use: "generate", Short: "generate code from the workspace's protobuf files", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := loadSession()
			if err != nil {
				return err
			}
			runner, err := plugrun.NativeRunner()
			if err != nil {
				return err
			}
			base, err := os.UserCacheDir()
			if err != nil {
				return fmt.Errorf("resolving the user cache directory for the plugin store (set XDG_CACHE_HOME or HOME): %w", err)
			}
			acq, err := plugoci.New(plugoci.Config{
				WorkDir: filepath.Join(base, "pb", "plugins"),
				Lock:    s.Lock,
				Policy:  s.Client.Policy,
			})
			if err != nil {
				return err
			}
			defer acq.Close()
			return dep.Gen(c.Context(), s, dep.GenDeps{Acquirer: acq, Runner: runner}, os.Stdout)
		},
	}
}

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

	session := loadSession
	run := func(f func(context.Context, *dep.Session) error) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			s, err := session()
			if err != nil {
				return err
			}
			return f(c.Context(), s)
		}
	}

	cmd.AddCommand(&cobra.Command{
		Use: "tidy", Short: "reconcile declarations with imports", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, s *dep.Session) error { return dep.Tidy(ctx, s) }),
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
		Use: "update [module path...]", Short: "move requirements to the highest discovered release",
		RunE: func(c *cobra.Command, args []string) error {
			s, err := session()
			if err != nil {
				return err
			}
			return dep.Update(c.Context(), s, os.Stdout, args...)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "why <module path>...", Short: "explain why modules are needed", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := session()
			if err != nil {
				return err
			}
			return dep.Why(c.Context(), s, os.Stdout, args...)
		},
	})
	return cmd
}

// workingTree roots the writable working tree at the filesystem root so
// the workspace walk can find roots above the working directory.
// Unix-shaped deliberately: a Windows port needs a drive-aware root and
// is not attempted here.
func workingTree() (billy.Filesystem, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	return osfs.New("/"), strings.TrimPrefix(filepath.ToSlash(cwd), "/"), nil
}

// assembleClient wires the fetch-verify pipeline from the environment:
// PBPROXY/PBNOPROXY (module-proxy.md REQ-proxy-config), PBCACHE
// (dep-verbs.md, the module cache term), and PBTRUSTEDROOT
// (provenance.md, the trusted root term). The lockfile and trust
// policy are the resolution root's and are wired by dep.Load.
func assembleClient() (*modfetch.Client, error) {
	sources, err := proxy.ParseConfig(os.Getenv("PBPROXY"), os.Getenv("PBNOPROXY"))
	if err != nil {
		return nil, err
	}
	cacheDir := os.Getenv("PBCACHE")
	if cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolving the user cache directory (set PBCACHE to override): %w", err)
		}
		cacheDir = filepath.Join(base, "pb", "mod")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	var root *gitprov.TrustedRoot
	if p := os.Getenv("PBTRUSTEDROOT"); p != "" {
		root, err = gitprov.LoadTrustedRoot(p)
		if err != nil {
			return nil, err
		}
	}
	httpClient := &http.Client{}
	return &modfetch.Client{
		HTTP:        httpClient,
		Sources:     sources,
		Cache:       &modfetch.Cache{FS: osfs.New(cacheDir)},
		Lock:        &lockfile.File{}, // replaced by dep.Load with the root's lockfile
		TrustedRoot: root,
		ResolveOrigin: func(ctx context.Context, modPath string) (origin.Origin, error) {
			return origin.Resolve(ctx, origin.Deps{Prober: &origin.GitProber{}, Client: httpClient}, modPath)
		},
		Fetcher: direct.Fetcher{},
	}, nil
}
