package fetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// The shared client fixture lives in internal/testing/fetchtest; the local
// wrapper assembles this package's Client over it, keeping the tests
// reading naturally.
type fixture struct {
	*fetchtest.Fixture
}

const (
	proxyHost = fetchtest.ProxyHost
	altHost   = fetchtest.AltHost
)

var gitWhen = fetchtest.GitWhen

func newFixture(t *testing.T) *fixture { return &fixture{fetchtest.New(t)} }

func moduleZip(t *testing.T, files map[string]string) ([]byte, string) {
	return fetchtest.ModuleZip(t, files)
}

// Client assembles the client under test over the fixture's transport,
// with a fresh in-memory cache and empty pin store. This is the one
// copy of fetchtest/assemble.Client the import cycle forces: this
// suite is package fetch, and assemble imports fetch.
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
func originOverride(fx *fetchtest.Fixture, repoURL string) {
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

// pinDigest is the archive digest the client's lockfile pins a pair
// at, the key the cache names the pair's entries by.
func pinDigest(t *testing.T, c *Client, modPath, v string) string {
	t.Helper()
	pin, ok := c.Lock.Module(modPath, v)
	if !ok {
		t.Fatalf("no pin for %s@%s", modPath, v)
	}
	return pin.Digest
}

// anyEntry reports whether the cache holds an entry of the kind for
// the pair under any digest: what "nothing cached" assertions ask.
func anyEntry(t *testing.T, c *Client, modPath string, v version.Version, kind string) bool {
	t.Helper()
	entries, err := c.Cache.FS.ReadDir(path.Join(proxy.Escape(modPath), VersionDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), proxy.Escape(v.String())+".") && strings.HasSuffix(e.Name(), "."+kind) {
			return true
		}
	}
	return false
}
