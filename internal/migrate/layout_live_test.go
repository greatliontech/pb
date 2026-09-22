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

// bsrDeps are the dependencies each table entry's BSR module declares,
// by BSR name: what its files import beyond the well-known imports,
// mapped through the table as a migration would map them — a name
// the table lacks (protoc-gen-validate, opencensus) leaves the
// entry's files short of an import, which the compile reports.
var bsrDeps = map[string][]string{
	"buf.build/grpc-ecosystem/grpc-gateway": {"buf.build/googleapis/googleapis"},
	"buf.build/envoyproxy/envoy":            {"buf.build/cncf/xds", "buf.build/envoyproxy/protoc-gen-validate", "buf.build/googleapis/googleapis", "buf.build/opencensus/opencensus", "buf.build/opentelemetry/opentelemetry", "buf.build/prometheus/client-model"},
	"buf.build/cncf/xds":                    {"buf.build/googleapis/googleapis", "buf.build/envoyproxy/protoc-gen-validate"},
	"buf.build/grpc/grpc":                   {"buf.build/googleapis/googleapis"},
}

// anchors are, per table entry, a file the BSR module serves at that
// import path: the module's files must sit at the paths buf's users
// import them by.
var anchors = map[string]string{
	"buf.build/opentelemetry/opentelemetry": "opentelemetry/proto/common/v1/common.proto",
	"buf.build/prometheus/client-model":     "io/prometheus/client/metrics.proto",
}

// TestDependencyLayouts verifies each dependency table entry's layout
// against its origin (REQ-migrate-deps): the module's own files
// compile from the named root with its BSR dependencies mapped through
// the table, and its anchor file sits at the import path the BSR
// served it at. It reaches the network and clones the origins, so it
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
			probe := &modfile.File{Module: "example.com/probe", Deps: map[string]string{}}
			for _, n := range append([]string{name}, bsrDeps[name]...) {
				path, ok := Dependencies[n]
				if !ok {
					t.Logf("%s depends on %s, which the table lacks", name, n)
					continue
				}
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
				if m.Path != Dependencies[name] {
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
				if _, err := compile.CompileOnly(ctx, mods, i); err != nil {
					t.Fatalf("%s at %s@%s: %v", name, m.Path, probe.Deps[m.Path], err)
				}
				t.Logf("%s: %d files compile at %s@%s", name, len(m.Protos()), m.Path, probe.Deps[m.Path])
				return
			}
			t.Fatalf("%s is not in the build", Dependencies[name])
		})
	}
}
