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
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/userconfig"
)

// openSession loads the machine's settings and the resolution session
// at the working directory under them: what a verb with no seam of its
// own between the two needs.
func openSession() (*userconfig.Settings, *dep.Session, error) {
	settings, err := userconfig.Load()
	if err != nil {
		return nil, nil, err
	}
	s, err := loadSession(settings)
	if err != nil {
		return nil, nil, err
	}
	return settings, s, nil
}

// loadSession assembles the resolution session at the working
// directory under the machine's settings: the working tree, the
// fetch-verify client, and the root's module files, lockfile, and
// trust policy.
func loadSession(settings *userconfig.Settings) (*dep.Session, error) {
	ws, dir, client, err := assembleTree(settings)
	if err != nil {
		return nil, err
	}
	return dep.Load(dep.Config{WS: ws, Dir: dir, Client: client})
}

// openTree is the working tree, the working directory within it and
// the fetch-verify client under the machine's settings, with no
// resolution root loaded: what a verb that writes the root needs.
func openTree() (billy.Filesystem, string, *fetch.Client, error) {
	settings, err := userconfig.Load()
	if err != nil {
		return nil, "", nil, err
	}
	return assembleTree(settings)
}

// assembleTree is the working tree, the working directory within it
// and the fetch-verify client under the settings given.
func assembleTree(settings *userconfig.Settings) (billy.Filesystem, string, *fetch.Client, error) {
	ws, dir, err := workingTree()
	if err != nil {
		return nil, "", nil, err
	}
	client, err := assembleClient(settings)
	if err != nil {
		return nil, "", nil, err
	}
	return ws, dir, client, nil
}

// rootOSPath is the resolution root's path on the host: the session's
// root is a path within the working tree, which workingTree roots at
// the host's filesystem root.
func rootOSPath(s *dep.Session) string {
	return filepath.Join(string(filepath.Separator), filepath.FromSlash(s.Root.Dir))
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
func assembleClient(settings *userconfig.Settings) (*fetch.Client, error) {
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
	return &fetch.Client{
		HTTP:        httpClient,
		Sources:     proxy.Config{Sources: sources, NoProxy: patterns},
		Cache:       &fetch.Cache{FS: osfs.New(cache.Value)},
		Lock:        &lockfile.File{}, // replaced by dep.Load with the root's lockfile
		TrustedRoot: root,
		ResolveOrigin: func(ctx context.Context, modPath string) (origin.Origin, error) {
			return origin.Resolve(ctx, origin.Deps{Prober: &origin.GitProber{}, Client: httpClient}, modPath)
		},
		Fetcher: direct.Fetcher{Store: osfs.New(filepath.Join(cache.Value, "vcs"))},
	}, nil
}
