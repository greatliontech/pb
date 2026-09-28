package dep

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// Clean empties the selected stores and nothing else (REQ-dep-clean):
// with no flag both, with a flag that store alone; the module cache's
// entries go, the vcs store keeps its origins' locks, the plugin
// store's emptying is asked once and its kept images reported; a
// cache absent is empty already.
func TestCleanEmptiesTheSelectedStores(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		modules, plugins bool
		wantModules      bool
		wantPlugins      bool
	}{
		"both by default": {false, false, true, true},
		"modules alone":   {true, false, true, false},
		"plugins alone":   {false, true, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			cache := osfs.New(scratchtest.Dir(t))
			origin := strings.Repeat("ab", 32)
			// Beside the cache's own: a file at the root, a stranger
			// under a module path, and a stranger under an @v
			// directory — the cache setting can name any directory,
			// and what the layout does not recognize is not the
			// cache's.
			for _, p := range []string{"example.com/m/@v/v1.0.0.zip", "example.com/m/@v/v1.0.0.mod", "example.com/m/@v/.put-tmp", "example.com/n/@v/v2.0.0.info", "example.com/n/@v/notes.txt", "example.com/o/README", "config.yaml", "vcs/" + origin + "/snapshots/HEAD", "vcs/" + origin + "/lock", "vcs/notes/snapshots/x", "go/pkg/mod/cache/download/other.org/x/@v/v1.0.0.zip"} {
				if err := util.WriteFile(cache, p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// Directories empty already — a git checkout's packed
			// refs leave such — and a nested tree with no @v are not
			// the cache's and are not touched.
			for _, d := range []string{"hooks", "proj/.git/refs/heads", "proj/.git/refs/tags"} {
				if err := cache.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			asked := 0
			stores := Stores{
				ModuleCache: cache,
				VCS:         osfs.New(filepath.Join(cache.Root(), "vcs")),
				Plugins: func(context.Context) ([]string, error) {
					asked++
					return []string{"sha256:" + strings.Repeat("ab", 32)}, nil
				},
			}
			var out bytes.Buffer
			if err := Clean(ctx, stores, &out, tc.modules, tc.plugins); err != nil {
				t.Fatalf("Clean: %v", err)
			}
			entries, err := cache.ReadDir(".")
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			vcs, err := cache.ReadDir("vcs/" + origin)
			if err != nil {
				t.Fatal(err)
			}
			var originEntries []string
			for _, e := range vcs {
				originEntries = append(originEntries, e.Name())
			}
			slices.Sort(names)
			slices.Sort(originEntries)
			left := func(paths ...string) {
				t.Helper()
				for _, p := range paths {
					if _, err := cache.Stat(p); err != nil {
						t.Fatalf("%s did not survive the emptying: %v", p, err)
					}
				}
			}
			if tc.wantModules {
				if strings.Join(names, ",") != "config.yaml,example.com,go,hooks,proj,vcs" || strings.Join(originEntries, ",") != "lock" {
					t.Fatalf("cache after the emptying: %v, origin %v; want the strangers and the vcs store, its origin's lock alone", names, originEntries)
				}
				left("example.com/n/@v/notes.txt", "example.com/o/README", "vcs/notes/snapshots/x", "hooks", "proj/.git/refs/heads", "proj/.git/refs/tags", "go/pkg/mod/cache/download/other.org/x/@v/v1.0.0.zip")
				if entries, err := cache.ReadDir("vcs/notes"); err != nil || len(entries) != 1 {
					t.Fatalf("a stranger under vcs was treated as an origin: %v, %v", entries, err)
				}
				if _, err := cache.Stat("example.com/m"); err == nil {
					t.Fatal("an emptied module path's directory survived")
				}
				if _, err := cache.Stat("example.com/n/@v/v2.0.0.info"); err == nil {
					t.Fatal("an artifact beside a stranger survived")
				}
				if !strings.Contains(out.String(), "modules: emptied\n") {
					t.Fatalf("report = %q", out.String())
				}
			} else if strings.Join(names, ",") != "config.yaml,example.com,go,hooks,proj,vcs" || strings.Join(originEntries, ",") != "lock,snapshots" || strings.Contains(out.String(), "modules:") {
				t.Fatalf("the module cache was touched: %v, origin %v, report %q", names, originEntries, out.String())
			} else {
				left("example.com/m/@v/v1.0.0.zip")
			}
			if tc.wantPlugins {
				if asked != 1 || !strings.Contains(out.String(), "plugins: emptied\nplugins: platform manifest sha256:"+strings.Repeat("ab", 32)+" kept by a live run or mount\n") {
					t.Fatalf("plugins asked %d times, report = %q", asked, out.String())
				}
			} else if asked != 0 || strings.Contains(out.String(), "plugins:") {
				t.Fatalf("the plugin store was touched: asked %d, report %q", asked, out.String())
			}
		})
	}

	// A cache absent is empty already; a plugin store's failure is
	// the verb's.
	absent := osfs.New(filepath.Join(scratchtest.Dir(t), "never"))
	failing := Stores{ModuleCache: absent, VCS: osfs.New(filepath.Join(absent.Root(), "vcs")), Plugins: func(context.Context) ([]string, error) {
		return nil, errors.New("held")
	}}
	var out bytes.Buffer
	if err := Clean(ctx, failing, &out, true, false); err != nil || out.String() != "modules: emptied\n" {
		t.Fatalf("Clean over an absent cache: %v, %q", err, out.String())
	}
	if err := Clean(ctx, failing, &out, false, true); err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("a plugin store's failure: %v", err)
	}
}
