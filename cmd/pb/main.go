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
	"github.com/greatliontech/pb/internal/pluglocal"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
	"github.com/greatliontech/pb/internal/proxy"
	"github.com/greatliontech/pb/internal/userconfig"
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
// directory under the machine's settings: the working tree, the
// fetch-verify client, and the root's module files, lockfile, and
// trust policy.
func loadSession(settings *userconfig.Settings) (*dep.Session, error) {
	ws, dir, err := workingTree()
	if err != nil {
		return nil, err
	}
	client, err := assembleClient(settings)
	if err != nil {
		return nil, err
	}
	return dep.Load(dep.Config{WS: ws, Dir: dir, Client: client})
}

// generateCmd is the generation verb (generation.md REQ-gen-verb): the
// selected runner — the --runner flag over the runner setting, the
// environment over the user configuration file, over the platform
// default (plugin-execution.md, REQ-plugin-runner-selection) — over
// pb's plugin store, which sits beside the module cache rather than
// inside it — the module cache root holds module artifacts only
// (dep-verbs.md REQ-dep-cache-layout).
func generateCmd() *cobra.Command {
	var runnerFlag string
	var overrides []string
	cmd := &cobra.Command{
		Use: "generate", Short: "generate code from the workspace's protobuf files", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var flag *string
			if c.Flags().Changed(plugrun.FlagRunner) {
				flag = &runnerFlag
			}
			settings, err := userconfig.Load()
			if err != nil {
				return err
			}
			runner, err := plugrun.Open(flag, settings.Get(userconfig.KeyRunner))
			if err != nil {
				return err
			}
			overrideMap := map[string]string{}
			for _, o := range overrides {
				ref, source, ok := strings.Cut(o, "=")
				if !ok || ref == "" || source == "" {
					return fmt.Errorf("--override %q: spelled REF=SOURCE", o)
				}
				if prev, dup := overrideMap[ref]; dup {
					return fmt.Errorf("--override %s given twice (%s and %s)", ref, prev, source)
				}
				overrideMap[ref] = source
			}
			s, err := loadSession(settings)
			if err != nil {
				return err
			}
			cfg, err := acquirerConfig(settings, runner, s)
			if err != nil {
				return err
			}
			acq, err := plugoci.New(cfg)
			if err != nil {
				return err
			}
			defer acq.Close()
			deps := dep.GenDeps{Acquirer: acq, Runner: runner, Diagnostics: os.Stderr, Overrides: overrideMap}
			// Local plugins run on the native runner wherever it exists.
			// The session's root is a path within the working tree,
			// which is rooted at the host's filesystem root.
			if native, err := plugrun.NativeRunner(); err == nil {
				root := filepath.Join(string(filepath.Separator), filepath.FromSlash(s.Root.Dir))
				deps.Local = &dep.LocalDeps{
					Acquirer: &pluglocal.Acquirer{Root: root, Lock: s.Lock, Policy: s.Client.Policy},
					Runner:   native,
				}
			}
			return dep.Gen(c.Context(), s, deps, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&runnerFlag, plugrun.FlagRunner, "", "runner for oci plugins: native or docker (over "+userconfig.Keys[userconfig.KeyRunner].Env+", over the user configuration file's runner key, over the platform default)")
	cmd.Flags().StringArrayVar(&overrides, "override", nil, "REF=SOURCE: run the oci plugin REF from SOURCE for this invocation — an OCI layout directory, an OCI layout archive or docker-save tarball, or docker://IMAGE (docker runner); repeatable")
	return cmd
}

// The plugin byte path's spellings (plugin-execution.md
// REQ-plugin-core-verifies).
const (
	pullStore  = "store"
	pullDaemon = "docker"
)

// acquirerConfig assembles the plugin acquirer: pb's plugin store,
// which sits beside the module cache rather than inside it (the
// module cache root holds module artifacts only, dep-verbs.md
// REQ-dep-cache-layout), the session's lockfile and trust policy,
// the runner's platform, and the byte path the settings select for
// that runner.
func acquirerConfig(settings *userconfig.Settings, runner plugrun.Runner, s *dep.Session) (plugoci.Config, error) {
	pull, err := pullMode(settings.Get(userconfig.KeyPluginPull), runner)
	if err != nil {
		return plugoci.Config{}, err
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return plugoci.Config{}, fmt.Errorf("resolving the user cache directory for the plugin store (set XDG_CACHE_HOME or HOME): %w", err)
	}
	os_, arch := runner.Platform()
	return plugoci.Config{
		WorkDir:     filepath.Join(base, "pb", "plugins"),
		Lock:        s.Lock,
		Policy:      s.Client.Policy,
		TrustedRoot: s.Client.TrustedRoot,
		Platform:    plugoci.Platform{OS: os_, Arch: arch},
		Pull:        pull,
	}, nil
}

// pullMode reads the plugin-pull setting: the store by default, the
// daemon where stated and the runner runs daemon images; a value
// naming neither byte path, or the daemon under a runner that runs
// none, is refused naming the layer the value came from.
func pullMode(v userconfig.Value, runner plugrun.Runner) (plugoci.PullMode, error) {
	switch v.Value {
	case "", pullStore:
		return plugoci.PullStore, nil
	case pullDaemon:
		if _, daemon := runner.(plugrun.DaemonImages); !daemon {
			return 0, v.Wrap(fmt.Errorf("plugin-pull %q: only the docker runner has a daemon to pull plugins (select it with --%s docker)", v.Value, plugrun.FlagRunner))
		}
		return plugoci.PullDaemon, nil
	}
	return 0, v.Wrap(fmt.Errorf("plugin-pull %q names no byte path (byte paths: %s, %s)", v.Value, pullStore, pullDaemon))
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

	session := func() (*dep.Session, error) {
		settings, err := userconfig.Load()
		if err != nil {
			return nil, err
		}
		return loadSession(settings)
	}
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

// assembleClient wires the fetch-verify pipeline from the settings —
// each the environment over the user configuration file, a refusal
// naming the layer the value came from (user-config.md): the proxy and
// its exclusions (module-proxy.md REQ-proxy-config), the module cache
// (dep-verbs.md, the module cache term), and the trusted root
// (provenance.md, the trusted root term). The lockfile and trust
// policy are the resolution root's and are wired by dep.Load.
func assembleClient(settings *userconfig.Settings) (*modfetch.Client, error) {
	proxyValue := settings.Get(userconfig.KeyProxy)
	sources, err := proxy.ParseSources(proxyValue.Value)
	if err != nil {
		return nil, proxyValue.Wrap(err)
	}
	noproxyValue := settings.Get(userconfig.KeyNoproxy)
	patterns, err := proxy.ParseNoProxy(noproxyValue.Value)
	if err != nil {
		return nil, noproxyValue.Wrap(err)
	}
	cache := settings.Get(userconfig.KeyCache)
	if !cache.Stated() {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolving the user cache directory (set the cache setting to override): %w", err)
		}
		cache = userconfig.Defaulted(filepath.Join(base, "pb", "mod"))
	}
	if err := os.MkdirAll(cache.Value, 0o755); err != nil {
		return nil, cache.Wrap(fmt.Errorf("module cache: %w", err))
	}
	var root *gitprov.TrustedRoot
	if p := settings.Get(userconfig.KeyTrustedRoot); p.Stated() {
		root, err = gitprov.LoadTrustedRoot(p.Value)
		if err != nil {
			return nil, p.Wrap(err)
		}
	}
	httpClient := &http.Client{}
	return &modfetch.Client{
		HTTP:        httpClient,
		Sources:     proxy.Config{Sources: sources, NoProxy: patterns},
		Cache:       &modfetch.Cache{FS: osfs.New(cache.Value)},
		Lock:        &lockfile.File{}, // replaced by dep.Load with the root's lockfile
		TrustedRoot: root,
		ResolveOrigin: func(ctx context.Context, modPath string) (origin.Origin, error) {
			return origin.Resolve(ctx, origin.Deps{Prober: &origin.GitProber{}, Client: httpClient}, modPath)
		},
		Fetcher: direct.Fetcher{},
	}, nil
}
