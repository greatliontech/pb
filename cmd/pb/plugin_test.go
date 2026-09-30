package main

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

// The build verb reads its flags into one request — a platform's tree,
// a base only for a platform given, each once — publishes to the
// reference through the ambient transport (a localhost registry is
// reached plain), and reports the list's digest and each platform's
// (REQ-publish-verb).
func TestPluginBuildCommand(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := "localhost:" + u.Port()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "protoc-gen-x"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		root := rootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"plugin", "build"}, args...))
		err := root.ExecuteContext(t.Context())
		return out.String(), err
	}
	ref := host + "/acme/x:v1"
	out, err := run(ref, "--entrypoint", "/protoc-gen-x", "--executable", "/protoc-gen-x", "--platform", "linux/amd64="+dir)
	if err != nil {
		t.Fatalf("pb plugin build: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], ref+"@sha256:") || !strings.HasSuffix(lines[0], " published") || !strings.HasPrefix(lines[1], "  linux/amd64 sha256:") {
		t.Fatalf("report = %q", out)
	}
	if out, err := run(ref, "--entrypoint", "/protoc-gen-x", "--executable", "/protoc-gen-x", "--platform", "linux/amd64="+dir); err != nil || !strings.HasSuffix(strings.Split(out, "\n")[0], " unchanged") {
		t.Fatalf("a rebuild: %q, %v", out, err)
	}
	for name, args := range map[string][]string{
		"a platform twice":     {ref, "--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64=" + dir, "--platform", "linux/amd64=" + dir},
		"a malformed platform": {ref, "--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64"},
		"a base with no tree":  {ref, "--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64=" + dir, "--base", "linux/arm64=" + host + "/acme/b@sha256:" + strings.Repeat("ab", 32)},
		"a base twice":         {ref, "--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64=" + dir, "--base", "linux/amd64=" + host + "/acme/b@sha256:" + strings.Repeat("ab", 32), "--base", "linux/amd64=" + host + "/acme/b@sha256:" + strings.Repeat("cd", 32)},
		"a malformed base":     {ref, "--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64=" + dir, "--base", "linux/amd64"},
		"no reference":         {"--entrypoint", "/protoc-gen-x", "--platform", "linux/amd64=" + dir},
	} {
		if _, err := run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
