package modfetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetchtest"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/version"
)

// The shared client fixture lives in internal/modfetchtest; the local
// wrapper assembles this package's Client over it, keeping the tests
// reading naturally.
type fixture struct {
	*modfetchtest.Fixture
}

const (
	proxyHost = modfetchtest.ProxyHost
	altHost   = modfetchtest.AltHost
)

var gitWhen = modfetchtest.GitWhen

func newFixture(t *testing.T) *fixture { return &fixture{modfetchtest.New(t)} }

func moduleZip(t *testing.T, files map[string]string) ([]byte, string) {
	return modfetchtest.ModuleZip(t, files)
}

// Client assembles the client under test over the fixture's transport,
// with a fresh in-memory cache and empty pin store. This is the one
// copy of modfetchtest/assemble.Client the import cycle forces: this
// suite is package modfetch, and assemble imports modfetch.
func (fx *fixture) Client(pbproxy string) *Client {
	return &Client{
		HTTP:          fx.HTTPClient(),
		Sources:       fx.Sources(pbproxy),
		Cache:         &Cache{FS: memfs.New()},
		Lock:          &lockfile.File{},
		ResolveOrigin: fx.Resolve,
		Fetcher:       fx.Fetcher(),
	}
}

// originOverride points every module path at the given repository URL
// (identity derivation input) while keeping subtree resolution.
func originOverride(fx *modfetchtest.Fixture, repoURL string) {
	fx.ResolveOverride = func(_ context.Context, modPath string) (origin.Origin, error) {
		return origin.Origin{Repo: repoURL, Subtree: fx.Subtrees[modPath]}, nil
	}
}

// tagPayload renders an unsigned annotated-tag body naming the commit.
func tagPayload(commit plumbing.Hash, name string) []byte {
	return []byte(fmt.Sprintf("object %s\ntype commit\ntag %s\n"+
		"tagger Test Signer <signer@example.com> 1700000100 +0000\n\nrelease %s\n",
		commit, name, name))
}

// envelope renders a provenance envelope carrying one git-signed-tag
// evidence object (REQ-proxy-prov-envelope).
func envelope(t *testing.T, format string, tag, commit []byte, treePath [][]byte) []byte {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	ev := map[string]any{
		"type":         "git-signed-tag",
		"objectFormat": format,
		"tag":          b64(tag),
		"commit":       b64(commit),
		"treePath":     []string{},
	}
	tp := make([]string, len(treePath))
	for i, raw := range treePath {
		tp[i] = b64(raw)
	}
	ev["treePath"] = tp
	doc, err := json.Marshal(map[string]any{"formatVersion": 1, "evidence": []any{ev}})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func ver(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// twoEvidenceEnvelope renders an envelope carrying two git-signed-tag
// evidence objects in order.
func twoEvidenceEnvelope(t *testing.T, tagA, tagB, commit []byte) []byte {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	mk := func(tag []byte) map[string]any {
		return map[string]any{
			"type": "git-signed-tag", "objectFormat": "sha1",
			"tag": b64(tag), "commit": b64(commit), "treePath": []string{},
		}
	}
	doc, err := json.Marshal(map[string]any{"formatVersion": 1, "evidence": []any{mk(tagA), mk(tagB)}})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// servedTag extracts the raw tag bytes of an envelope's first evidence
// object.
func servedTag(t *testing.T, env []byte) []byte {
	t.Helper()
	var doc struct {
		Evidence []struct {
			Tag string `json:"tag"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(env, &doc); err != nil || len(doc.Evidence) == 0 {
		t.Fatalf("parsing served envelope: %v", err)
	}
	b, err := base64.StdEncoding.DecodeString(doc.Evidence[0].Tag)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
