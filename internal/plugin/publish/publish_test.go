package publish

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"pgregory.net/rapid"
)

var ctx = context.Background()

// A registry in this process, reached as localhost:<port> — a host the
// reference grammar admits (generation.md REQ-gen-schema).
func testRegistry(t *testing.T) (host string, transport http.RoundTripper) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return "localhost:" + u.Port(), srv.Client().Transport
}

// tree writes a platform tree: files by tree-relative path, a name
// with a trailing "*" executable.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// entries reads a layer's tar as name -> (mode, content).
func entries(t *testing.T, layer v1.Layer) map[string][2]string {
	t.Helper()
	out := map[string][2]string{}
	for _, e := range ordered(t, layer) {
		out[e[0]] = [2]string{e[1], e[2]}
	}
	return out
}

// ordered reads a layer's tar in its order as (name, mode, content).
func ordered(t *testing.T, layer v1.Layer) [][3]string {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	var out [][3]string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// The zero time in a tar header is the epoch.
		if h.Uid != 0 || h.Gid != 0 || h.ModTime.Unix() != 0 || h.Uname != "" || h.Gname != "" {
			t.Fatalf("entry %s: uid %d gid %d mtime %v names %q %q, want 0:0 at the epoch, unnamed", h.Name, h.Uid, h.Gid, h.ModTime, h.Uname, h.Gname)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, [3]string{h.Name, h.FileInfo().Mode().String(), string(b)})
	}
	return out
}

// The layer's entries come in sorted path order — the global order of
// the tree-relative paths, not a walk's — so the digest is the tree's
// alone (REQ-publish-image, REQ-publish-determinism).
func TestLayerEntriesAreInSortedPathOrder(t *testing.T) {
	dir := tree(t, map[string]string{"a/b": "1", "a-b": "2", "a.txt": "3", "protoc-gen-x*": "bin", "z/y/x": "4", "z/w": "5"})
	layer, err := layerOf(dir, "/protoc-gen-x", map[string]bool{"protoc-gen-x": true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ordered(t, layer) {
		names = append(names, e[0])
	}
	if got, want := strings.Join(names, " "), "a-b a.txt a/ a/b protoc-gen-x z/ z/w z/y/ z/y/x"; got != want {
		t.Fatalf("entries = %q, want %q", got, want)
	}
}

// Build publishes one image per platform over its tree — one layer
// in canonical form, the configuration the contract names — and the
// list over exactly the platforms given, its digest a function of the
// inputs (REQ-publish-verb, REQ-publish-image, REQ-publish-list,
// REQ-publish-determinism).
func TestBuildPublishesTheListOverTheTrees(t *testing.T) {
	host, transport := testRegistry(t)
	// The host's mode bits are no input: the entrypoint is written
	// non-executable here and the helper executable, and the image
	// says the opposite, as the build names it.
	amd := tree(t, map[string]string{"protoc-gen-x": "amd64 binary", "lib/data.txt": "shared", "lib/helper*": "helper"})
	arm := tree(t, map[string]string{"protoc-gen-x": "arm64 binary", "lib/data.txt": "shared", "lib/helper*": "helper"})
	ref := host + "/acme/x:v1"
	req := Request{
		Reference: ref, Entrypoint: []string{"/protoc-gen-x", "--flag"},
		Platforms: map[string]Tree{"linux/arm64": {Dir: arm}, "linux/amd64": {Dir: amd}},
		Transport: transport,
	}
	got, err := Build(ctx, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got.Unchanged || len(got.Images) != 2 || got.Images[0].Platform != "linux/amd64" || got.Images[1].Platform != "linux/arm64" {
		t.Fatalf("published = %+v", got)
	}
	tag, _ := name.NewTag(ref, name.StrictValidation)
	idx, err := remote.Index(tag, remote.WithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := idx.Digest()
	if d.String() != got.Digest {
		t.Fatalf("the registry holds %s, the report says %s", d, got.Digest)
	}
	mt, _ := idx.MediaType()
	if mt != types.OCIImageIndex {
		t.Fatalf("list media type = %s", mt)
	}
	man, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Manifests) != 2 || man.Manifests[0].Platform.String() != "linux/amd64" || man.Manifests[1].Platform.String() != "linux/arm64" {
		t.Fatalf("list entries = %+v", man.Manifests)
	}
	for i, m := range man.Manifests {
		if m.Digest.String() != got.Images[i].Digest {
			t.Fatalf("entry %d digest %s, reported %s", i, m.Digest, got.Images[i].Digest)
		}
		img, err := idx.Image(m.Digest)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OS != "linux" || cfg.Architecture != m.Platform.Architecture || cfg.Variant != "" ||
			strings.Join(cfg.Config.Entrypoint, " ") != "/protoc-gen-x --flag" || len(cfg.Config.Cmd) != 0 ||
			cfg.Config.User != "65534:65534" || cfg.Config.WorkingDir != "/" || len(cfg.Config.Env) != 0 ||
			!cfg.Created.IsZero() || len(cfg.History) != 0 {
			t.Fatalf("config for %s = %+v", m.Platform, cfg)
		}
		layers, err := img.Layers()
		if err != nil || len(layers) != 1 {
			t.Fatalf("layers = %d, %v", len(layers), err)
		}
		if lmt, _ := layers[0].MediaType(); lmt != types.OCIUncompressedLayer {
			t.Fatalf("layer media type = %s, want uncompressed", lmt)
		}
		want := map[string][2]string{
			"lib/":         {"drwxr-xr-x", ""},
			"lib/data.txt": {"-rw-r--r--", "shared"},
			"lib/helper":   {"-rw-r--r--", "helper"},
			"protoc-gen-x": {"-rwxr-xr-x", m.Platform.Architecture + " binary"},
		}
		if got := entries(t, layers[0]); len(got) != len(want) {
			t.Fatalf("layer entries = %v", got)
		} else {
			for n, w := range want {
				if got[n] != w {
					t.Fatalf("entry %s = %v, want %v", n, got[n], w)
				}
			}
		}
	}

	// The same inputs elsewhere on disk, with other mode bits and
	// through a link to the tree, rebuild the same digest: the tag
	// holds it already and nothing is published.
	amd2 := tree(t, map[string]string{"protoc-gen-x*": "amd64 binary", "lib/data.txt*": "shared", "lib/helper": "helper"})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(amd2, link); err != nil {
		t.Fatal(err)
	}
	again := Request{
		Reference: ref, Entrypoint: []string{"/protoc-gen-x", "--flag"},
		Platforms: map[string]Tree{
			"linux/amd64": {Dir: link},
			"linux/arm64": {Dir: tree(t, map[string]string{"protoc-gen-x": "arm64 binary", "lib/data.txt": "shared", "lib/helper": "helper"})},
		},
		Transport: transport,
	}
	same, err := Build(ctx, again)
	if err != nil || !same.Unchanged || same.Digest != got.Digest {
		t.Fatalf("a rebuild: %+v, %v", same, err)
	}
	// One byte changed, or a file named executable, is another
	// digest, which the tag refuses.
	again.Platforms["linux/amd64"] = Tree{Dir: tree(t, map[string]string{"protoc-gen-x": "amd64 binary v2", "lib/data.txt": "shared", "lib/helper": "helper"})}
	if _, err := Build(ctx, again); !errors.Is(err, ErrTagTaken) {
		t.Fatalf("a changed build over a held tag: %v", err)
	}
	again.Platforms["linux/amd64"] = Tree{Dir: amd2}
	again.Executables = []string{"/lib/helper"}
	if _, err := Build(ctx, again); !errors.Is(err, ErrTagTaken) {
		t.Fatalf("a file named executable over a held tag: %v", err)
	}
	// Named executable at a fresh tag, the helper is 0755.
	again.Reference = host + "/acme/x:v2"
	if _, err := Build(ctx, again); err != nil {
		t.Fatal(err)
	}
	tag2, _ := name.NewTag(again.Reference, name.StrictValidation)
	idx2, err := remote.Index(tag2, remote.WithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	man2, _ := idx2.IndexManifest()
	img2, _ := idx2.Image(man2.Manifests[0].Digest)
	layers2, _ := img2.Layers()
	if e := entries(t, layers2[0]); e["lib/helper"][0] != "-rwxr-xr-x" || e["lib/data.txt"][0] != "-rw-r--r--" {
		t.Fatalf("executables = %v", e)
	}
}

// Over a base, the base's layers precede the tree's and its
// configuration stands but for the entrypoint, cmd and platform
// (REQ-publish-image).
func TestBuildOverABase(t *testing.T) {
	host, transport := testRegistry(t)
	base := v1.Image(empty.Image)
	base, err := mutate.AppendLayers(base, tarLayer(t, "runtime", "a runtime"))
	if err != nil {
		t.Fatal(err)
	}
	base, err = mutate.ConfigFile(base, &v1.ConfigFile{OS: "linux", Architecture: "amd64", Created: v1.Time{Time: time.Unix(1700000000, 0)}, Config: v1.Config{
		Entrypoint: []string{"/runtime"}, Cmd: []string{"--serve"}, Env: []string{"JAVA_HOME=/jvm"}, User: "1000", WorkingDir: "/app",
	}, History: []v1.History{{CreatedBy: "the base"}}})
	if err != nil {
		t.Fatal(err)
	}
	// A windows base carries its OS version, which the list's entry
	// names as the configuration does.
	winBase, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "windows", Architecture: "amd64", OSVersion: "10.0.20348.1", Config: v1.Config{Entrypoint: []string{"/runtime.exe"}}})
	if err != nil {
		t.Fatal(err)
	}
	winTag, _ := name.NewTag(host+"/acme/winruntime:1", name.StrictValidation)
	if err := remote.Write(winTag, winBase, remote.WithTransport(transport)); err != nil {
		t.Fatal(err)
	}
	wd, _ := winBase.Digest()
	winReq := Request{
		Reference: host + "/acme/w:v1", Entrypoint: []string{"/plugin.exe"},
		Platforms: map[string]Tree{"windows/amd64": {Dir: tree(t, map[string]string{"plugin.exe": "exe"}), Base: host + "/acme/winruntime@" + wd.String()}},
		Transport: transport,
	}
	if _, err := Build(ctx, winReq); err != nil {
		t.Fatal(err)
	}
	wTag, _ := name.NewTag(winReq.Reference, name.StrictValidation)
	wIdx, err := remote.Index(wTag, remote.WithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	wMan, _ := wIdx.IndexManifest()
	if p := wMan.Manifests[0].Platform; p.OS != "windows" || p.OSVersion != "10.0.20348.1" {
		t.Fatalf("the windows entry's platform = %+v, want the base's OS version carried", p)
	}
	if err != nil {
		t.Fatal(err)
	}
	baseTag, _ := name.NewTag(host+"/acme/runtime:1", name.StrictValidation)
	if err := remote.Write(baseTag, base, remote.WithTransport(transport)); err != nil {
		t.Fatal(err)
	}
	bd, _ := base.Digest()
	req := Request{
		Reference: host + "/acme/y:v1", Entrypoint: []string{"/plugin.jar"},
		Platforms: map[string]Tree{"linux/amd64": {Dir: tree(t, map[string]string{"plugin.jar": "jar"}), Base: host + "/acme/runtime@" + bd.String()}},
		Transport: transport,
	}
	got, err := Build(ctx, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tag, _ := name.NewTag(req.Reference, name.StrictValidation)
	idx, err := remote.Index(tag, remote.WithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	man, _ := idx.IndexManifest()
	img, err := idx.Image(man.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := img.ConfigFile()
	if strings.Join(cfg.Config.Entrypoint, " ") != "/plugin.jar" || len(cfg.Config.Cmd) != 0 ||
		strings.Join(cfg.Config.Env, ",") != "JAVA_HOME=/jvm" || cfg.Config.User != "1000" || cfg.Config.WorkingDir != "/app" ||
		!cfg.Created.IsZero() || len(cfg.History) != 0 {
		t.Fatalf("config over the base = %+v", cfg)
	}
	layers, _ := img.Layers()
	if len(layers) != 2 {
		t.Fatalf("%d layers, want the base's and the tree's", len(layers))
	}
	if e := entries(t, layers[1]); e["plugin.jar"][1] != "jar" {
		t.Fatalf("the tree's layer = %v", e)
	}
	if e := entries(t, layers[0]); e["plugin.jar"][1] != "" || e["runtime"][1] != "a runtime" {
		t.Fatal("the base's layer holds the tree")
	}
	_ = got
	// A base named without a digest, or absent, refuses the build.
	req.Reference = host + "/acme/z:v1"
	req.Platforms["linux/amd64"] = Tree{Dir: tree(t, map[string]string{"plugin.jar": "jar"}), Base: host + "/acme/runtime:1"}
	if _, err := Build(ctx, req); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("a base without a digest: %v", err)
	}
	// A digest naming one image is that image whatever platform is
	// asked for: an amd64 base under an arm64 tree is refused.
	req.Platforms = map[string]Tree{"linux/arm64": {Dir: tree(t, map[string]string{"plugin.jar": "jar"}), Base: host + "/acme/runtime@" + bd.String()}}
	if _, err := Build(ctx, req); err == nil || !strings.Contains(err.Error(), "is linux/amd64, not linux/arm64") {
		t.Fatalf("a base of another platform: %v", err)
	}
}

// Every refusal comes before anything is published (REQ-publish-verb).
func TestBuildRefusals(t *testing.T) {
	host, transport := testRegistry(t)
	ok := tree(t, map[string]string{"protoc-gen-x*": "bin"})
	linked := tree(t, map[string]string{"protoc-gen-x*": "bin"})
	if err := os.Symlink("protoc-gen-x", filepath.Join(linked, "alias")); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		req  Request
		want string
	}{
		"no tag":                      {Request{Reference: host + "/acme/x", Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "tag"},
		"a digest":                    {Request{Reference: host + "/acme/x@sha256:" + strings.Repeat("ab", 32), Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "digest"},
		"no registry":                 {Request{Reference: "acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "registry"},
		"relative entrypoint":         {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "absolute path"},
		"relative executable":         {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Executables: []string{"lib/x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "--executable"},
		"executable absent":           {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Executables: []string{"/lib/helpr"}, Platforms: map[string]Tree{"linux/amd64": {Dir: tree(t, map[string]string{"protoc-gen-x": "bin", "lib/helper": "h"})}}}, "--executable /lib/helpr names no regular file"},
		"executable directory":        {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Executables: []string{"/lib"}, Platforms: map[string]Tree{"linux/amd64": {Dir: tree(t, map[string]string{"protoc-gen-x": "bin", "lib/helper": "h"})}}}, "--executable /lib names no regular file"},
		"executable in one tree only": {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Executables: []string{"/lib/helper"}, Platforms: map[string]Tree{"linux/amd64": {Dir: tree(t, map[string]string{"protoc-gen-x": "bin", "lib/helper": "h"})}, "linux/arm64": {Dir: ok}}}, "linux/arm64: --executable /lib/helper names no regular file"},
		"no platform":                 {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}}, "no --platform"},
		"unknown platform":            {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"plan9/mips": {Dir: ok}}}, "names no platform"},
		"entrypoint absent":           {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-y"}, Platforms: map[string]Tree{"linux/amd64": {Dir: ok}}}, "names no regular file"},
		"a symbolic link":             {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: linked}}}, "symbolic link"},
		"not a directory":             {Request{Reference: host + "/acme/x:v1", Entrypoint: []string{"/protoc-gen-x"}, Platforms: map[string]Tree{"linux/amd64": {Dir: filepath.Join(ok, "protoc-gen-x")}}}, "not a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.req.Transport = transport
			if _, err := Build(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Build = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
	tag, _ := name.NewTag(host+"/acme/x:v1", name.StrictValidation)
	if _, err := remote.Head(tag, remote.WithTransport(transport)); err == nil {
		t.Fatal("a refused build published the tag")
	}
}

// tarLayer is a one-file layer for a base image.
func tarLayer(t *testing.T, name, content string) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

// The command line's values become one request, each refusal naming
// its flag (REQ-publish-verb).
func TestParseRequest(t *testing.T) {
	req, err := ParseRequest("r.io/a/x:v1", []string{"/x"}, []string{"/h"}, []string{"linux/amd64=d1", "linux/arm64=d2"}, []string{"linux/arm64=r.io/b@sha256:" + strings.Repeat("ab", 32)})
	if err != nil || req.Platforms["linux/amd64"] != (Tree{Dir: "d1"}) || req.Platforms["linux/arm64"] != (Tree{Dir: "d2", Base: "r.io/b@sha256:" + strings.Repeat("ab", 32)}) || len(req.Executables) != 1 {
		t.Fatalf("request = %+v, %v", req, err)
	}
	for name, tc := range map[string]struct {
		platforms, bases []string
		want             string
	}{
		"a platform twice":     {[]string{"linux/amd64=d", "linux/amd64=d"}, nil, "--platform linux/amd64 given twice"},
		"a malformed platform": {[]string{"linux/amd64"}, nil, "--platform \"linux/amd64\" is not"},
		"a base with no tree":  {[]string{"linux/amd64=d"}, []string{"linux/arm64=r.io/b@sha256:x"}, "--base linux/arm64 names a platform no --platform"},
		"a base twice":         {[]string{"linux/amd64=d"}, []string{"linux/amd64=r.io/b@sha256:x", "linux/amd64=r.io/c@sha256:y"}, "--base linux/amd64 given twice"},
		"a malformed base":     {[]string{"linux/amd64=d"}, []string{"linux/amd64"}, "--base \"linux/amd64\" is not"},
	} {
		if _, err := ParseRequest("r.io/a/x:v1", []string{"/x"}, nil, tc.platforms, tc.bases); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// Property: the digests are a function of the inputs alone. A tree
// generated at random is written twice — into other directories, with
// other host mode bits and other timestamps, one through a link — and
// assembled twice with the platforms given in another order; every
// image digest and the list's are the same, and one byte of content
// changes them (REQ-publish-determinism).
func TestPropertyDigestsAreAFunctionOfTheInputs(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// A name that is a directory prefix of another cannot be a
		// file too: the drawn set is made prefix-free, the shorter
		// name dropped, at least one name kept.
		drawn := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z][a-z0-9]{0,4}(/[a-z][a-z0-9]{0,4}){0,2}`), 1, 6, rapid.ID[string]).Draw(rt, "names")
		var names []string
		for _, n := range drawn {
			prefix := false
			for _, m := range drawn {
				if strings.HasPrefix(m, n+"/") {
					prefix = true
				}
			}
			if !prefix {
				names = append(names, n)
			}
		}
		contents := map[string]string{}
		for _, n := range names {
			contents[n] = rapid.String().Draw(rt, "content of "+n)
		}
		entry := names[0]
		var executables []string
		for _, n := range names[1:] {
			if rapid.Bool().Draw(rt, "exec "+n) {
				executables = append(executables, "/"+n)
			}
		}
		platforms := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"linux/amd64", "linux/arm64", "darwin/arm64", "windows/amd64"}), 1, 3, rapid.ID[string]).Draw(rt, "platforms")
		write := func(mutateHost bool) string {
			dir := t.TempDir()
			for n, c := range contents {
				full := filepath.Join(dir, filepath.FromSlash(n))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					rt.Fatal(err)
				}
				mode := os.FileMode(0o644)
				if mutateHost && rapid.Bool().Draw(rt, "host bit "+n) {
					mode = 0o755
				}
				if err := os.WriteFile(full, []byte(c), mode); err != nil {
					rt.Fatal(err)
				}
				if mutateHost {
					when := time.Unix(rapid.Int64Range(0, 2_000_000_000).Draw(rt, "mtime "+n), 0)
					if err := os.Chtimes(full, when, when); err != nil {
						rt.Fatal(err)
					}
				}
			}
			return dir
		}
		build := func(dir string, reverse bool) []string {
			req := Request{Reference: "r.io/a/x:v1", Entrypoint: []string{"/" + entry, "--arg"}, Executables: executables, Platforms: map[string]Tree{}}
			ps := slices.Clone(platforms)
			if reverse {
				slices.Reverse(ps)
			}
			for _, p := range ps {
				req.Platforms[p] = Tree{Dir: dir}
			}
			idx, images, err := assemble(req, nil)
			if err != nil {
				rt.Fatal(err)
			}
			d, _ := idx.Digest()
			out := []string{d.String()}
			for _, i := range images {
				out = append(out, i.Platform+"="+i.Digest)
			}
			return out
		}
		first := build(write(false), false)
		link := filepath.Join(t.TempDir(), "tree")
		if err := os.Symlink(write(true), link); err != nil {
			rt.Fatal(err)
		}
		second := build(link, true)
		if !slices.Equal(first, second) {
			rt.Fatalf("digests differ:\n%v\n%v", first, second)
		}
		// One byte of content is another digest.
		contents[entry] += "x"
		if third := build(write(false), false); slices.Equal(first, third) {
			rt.Fatalf("a changed content kept the digests %v", first)
		}
	})
}

// A base digest naming an index yields the index's one entry for the
// platform; two entries for it, or none, refuse the base — the choice
// is the publisher's, never a library's pick (REQ-publish-verb,
// REQ-publish-determinism).
func TestBuildOverAnIndexBase(t *testing.T) {
	host, transport := testRegistry(t)
	// entries are platform spellings, `os/arch[/variant]`, each child
	// marked by its index so two of one platform differ in content.
	child := func(i int, pl v1.Platform) v1.Image {
		img, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: pl.OS, Architecture: pl.Architecture, Variant: pl.Variant, Config: v1.Config{Entrypoint: []string{"/runtime"}, Env: []string{fmt.Sprintf("CHILD=%d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	push := func(repo string, entries ...string) string {
		idx := v1.ImageIndex(empty.Index)
		for i, e := range entries {
			parts := strings.SplitN(e, "/", 3)
			pl := v1.Platform{OS: parts[0], Architecture: parts[1]}
			if len(parts) == 3 {
				pl.Variant = parts[2]
			}
			idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: child(i, pl), Descriptor: v1.Descriptor{Platform: &pl}})
		}
		tag, _ := name.NewTag(host+"/acme/"+repo+":1", name.StrictValidation)
		if err := remote.WriteIndex(tag, idx, remote.WithTransport(transport)); err != nil {
			t.Fatal(err)
		}
		d, _ := idx.Digest()
		return host + "/acme/" + repo + "@" + d.String()
	}
	request := func(base string) Request {
		return Request{Reference: host + "/acme/z:v1", Entrypoint: []string{"/plugin"}, Platforms: map[string]Tree{"linux/amd64": {Dir: tree(t, map[string]string{"plugin": "bin"}), Base: base}}, Transport: transport}
	}
	if _, err := Build(ctx, request(push("one", "linux/amd64", "linux/arm64"))); err != nil {
		t.Fatalf("an index with one entry for the platform: %v", err)
	}
	// The variant is not consulted: an entry under one is the
	// platform's entry where it is the only one, and two under
	// different variants are two.
	if _, err := Build(ctx, request(push("variant", "linux/amd64/v9", "linux/arm64"))); err != nil {
		t.Fatalf("an index whose one entry for the platform carries a variant: %v", err)
	}
	if _, err := Build(ctx, request(push("two", "linux/amd64", "linux/amd64"))); err == nil || !strings.Contains(err.Error(), "2 entries for linux/amd64") {
		t.Fatalf("an index with two entries for the platform: %v", err)
	}
	if _, err := Build(ctx, request(push("variants", "linux/amd64/v8", "linux/amd64/v9"))); err == nil || !strings.Contains(err.Error(), "2 entries for linux/amd64") {
		t.Fatalf("an index with two entries for the platform under variants: %v", err)
	}
	if _, err := Build(ctx, request(push("none", "linux/arm64"))); err == nil || !strings.Contains(err.Error(), "0 entries for linux/amd64") {
		t.Fatalf("an index with no entry for the platform: %v", err)
	}
}
