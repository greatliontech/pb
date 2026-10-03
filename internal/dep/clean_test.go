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
// with no flag every store, with a flag that store alone; the module
// cache's entries go, the vcs store keeps its origins' locks, the
// plugin store's emptying is asked once and its kept images reported,
// the source store's copies and temporaries go and its strangers
// stay; a cache absent is empty already.
func TestCleanEmptiesTheSelectedStores(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		modules, plugins, sources bool
		wantModules               bool
		wantPlugins               bool
		wantSources               bool
	}{
		"every store by default": {false, false, false, true, true, true},
		"modules alone":          {true, false, false, true, false, false},
		"plugins alone":          {false, true, false, false, true, false},
		"sources alone":          {false, false, true, false, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			cache := osfs.New(scratchtest.Dir(t))
			sources := osfs.New(scratchtest.Dir(t))
			// The source store's copies, a temporary an interrupted
			// write left, and strangers: a file, a directory no copy's
			// name spells, a name whose halves are not escaped.
			for _, p := range []string{"example.com/m@v1.0.0/m.proto", "example.com/m@v1.0.0/sub/x.proto", "github.com/!org/n@v2.0.0/n.proto", "well-known@" + strings.Repeat("0f", 32) + "/google/protobuf/any.proto", ".pb-sources-123", "notes.txt", "stranger/x.proto", "Bad@v1.0.0/x.proto", "noversion@/x.proto", "photos@2024/img.proto", "example.com/m@latest/m.proto", "well-known@abc/x.proto"} {
				// A copy's files are read-only (the store's mode); the
				// emptying removes them all the same.
				if err := util.WriteFile(sources, p, []byte("x"), 0o444); err != nil {
					t.Fatal(err)
				}
			}
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
				Sources: sources,
			}
			var out bytes.Buffer
			if err := Clean(ctx, stores, &out, tc.modules, tc.plugins, tc.sources); err != nil {
				t.Fatalf("Clean: %v", err)
			}
			sourceEntries, err := sources.ReadDir(".")
			if err != nil {
				t.Fatal(err)
			}
			var sourceNames []string
			for _, e := range sourceEntries {
				sourceNames = append(sourceNames, e.Name())
			}
			slices.Sort(sourceNames)
			if tc.wantSources {
				if left, err := sources.ReadDir("example.com"); err != nil || len(left) != 1 || left[0].Name() != "m@latest" {
					t.Fatalf("under example.com after the emptying: %v, %v; want the stranger m@latest alone", left, err)
				}
				if strings.Join(sourceNames, ",") != "Bad@v1.0.0,example.com,notes.txt,noversion@,photos@2024,stranger,well-known@abc" || !strings.Contains(out.String(), "sources: emptied\n") {
					t.Fatalf("the source store after the emptying: %v, report %q; want the strangers alone", sourceNames, out.String())
				}
			} else if strings.Join(sourceNames, ",") != ".pb-sources-123,Bad@v1.0.0,example.com,github.com,notes.txt,noversion@,photos@2024,stranger,well-known@"+strings.Repeat("0f", 32)+",well-known@abc" || strings.Contains(out.String(), "sources:") {
				t.Fatalf("the source store was touched: %v, report %q", sourceNames, out.String())
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
	if err := Clean(ctx, failing, &out, true, false, false); err != nil || out.String() != "modules: emptied\n" {
		t.Fatalf("Clean over an absent cache: %v, %q", err, out.String())
	}
	if err := Clean(ctx, failing, &out, false, true, false); err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("a plugin store's failure: %v", err)
	}
	out.Reset()
	failing.Sources = osfs.New(filepath.Join(absent.Root(), "sources"))
	if err := Clean(ctx, failing, &out, false, false, true); err != nil || out.String() != "sources: emptied\n" {
		t.Fatalf("Clean over an absent source store: %v, %q", err, out.String())
	}
}
