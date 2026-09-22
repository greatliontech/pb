package migrate

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// anchors are, per table entry, a file the BSR module serves at that
// import path: the module's files must sit at the paths buf's users
// import them by.
var anchors = map[string]string{
	"buf.build/googleapis/googleapis":       "google/api/annotations.proto",
	"buf.build/grpc-ecosystem/grpc-gateway": "protoc-gen-openapiv2/options/annotations.proto",
	"buf.build/grpc/grpc":                   "grpc/health/v1/health.proto",
	"buf.build/opentelemetry/opentelemetry": "opentelemetry/proto/common/v1/common.proto",
	"buf.build/prometheus/client-model":     "io/prometheus/client/metrics.proto",
}

// TestDependencyLayouts verifies each dependency table entry's layout
// against its origin (REQ-migrate-deps): every file of the module
// resolves its imports from the named root, within the module, its
// BSR dependencies mapped through the table, or the well-known
// imports, and its anchor file — one buf's users import at that path
// — sits at that path and compiles. The module's files are not
// compiled as one set: a repository may hold files no one compiles
// together (googleapis' preview tree redeclares its packages), and a
// build compiles only what a user's files import. It reaches the
// network and clones the origins, so it
// runs only where PB_LIVE_ORIGINS is set — at an entry's addition, by
// hand; set to a directory's absolute path, that directory is the
// module cache the run keeps, so a second run clones nothing twice.
func TestDependencyLayouts(t *testing.T) {
	live := os.Getenv("PB_LIVE_ORIGINS")
	if live == "" {
		t.Skip("PB_LIVE_ORIGINS unset: the origins are not reached")
	}
	ctx := context.Background()
	cache := live
	if !filepath.IsAbs(cache) {
		cache = scratchtest.Dir(t)
	}
	sources, err := proxy.ParseSources("direct")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{}
	client := &fetch.Client{
		HTTP:    httpClient,
		Sources: proxy.Config{Sources: sources},
		Cache:   &fetch.Cache{FS: osfs.New(cache)},
		Lock:    &lockfile.File{},
		ResolveOrigin: func(ctx context.Context, modPath string) (origin.Origin, error) {
			return origin.Resolve(ctx, origin.Deps{Prober: &origin.GitProber{}, Client: httpClient}, modPath)
		},
		Fetcher: direct.Fetcher{Store: osfs.New(filepath.Join(cache, "vcs"))},
	}
	names := make([]string, 0, len(Dependencies))
	for n := range Dependencies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			// A probe workspace declaring the entry and its BSR
			// dependencies, each at the latest version discovered.
			ws := scratchtest.Dir(t)
			// A probe workspace declaring the entry and, as a migration
			// would, the entries it imports to closure.
			probe := &modfile.File{Module: "example.com/probe", Deps: map[string]string{}}
			closure := []string{name}
			for i := 0; i < len(closure); i++ {
				for _, d := range Dependencies[closure[i]].Deps {
					if !slices.Contains(closure, d) {
						closure = append(closure, d)
					}
				}
			}
			for _, n := range closure {
				path := Dependencies[n].Path
				v, err := client.Latest(ctx, path)
				if err != nil {
					t.Fatalf("latest of %s: %v", path, err)
				}
				probe.Deps[path] = v.String()
			}
			b, err := modfile.Encode(probe)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ws, "pb.yaml"), b, 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := dep.Load(dep.Config{WS: osfs.New(ws), Dir: ".", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			_, mods, err := s.Modules(ctx)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			for i, m := range mods {
				if m.Path != Dependencies[name].Path {
					continue
				}
				if len(m.Protos()) == 0 {
					t.Fatalf("%s holds no proto file at %s", name, m.Path)
				}
				anchor, ok := anchors[name]
				if !ok {
					t.Fatalf("%s: no anchor file named for the entry", name)
				}
				if !slices.Contains(m.Protos(), anchor) {
					t.Fatalf("%s: %q is not among the module's files at %s", name, anchor, m.Path)
				}
				if _, err := compile.CompileFiles(ctx, mods, i, []string{anchor}); err != nil {
					t.Fatalf("%s at %s@%s: %v", name, m.Path, probe.Deps[m.Path], err)
				}
				t.Logf("%s: %d files resolve their imports and %s compiles at %s@%s", name, len(m.Protos()), anchor, m.Path, probe.Deps[m.Path])
				return
			}
			t.Fatalf("%s is not in the build", Dependencies[name].Path)
		})
	}
}
