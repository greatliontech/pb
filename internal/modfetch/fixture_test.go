package modfetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/direct"
	"github.com/greatliontech/pb/internal/gittest"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/proxy"
	"github.com/greatliontech/pb/internal/version"
)

// proxyHost and altHost are the fixture's in-process proxy hosts:
// "proxy" and "alt" in a client's source list resolve to them.
const (
	proxyHost = "proxy.test"
	altHost   = "alt.test"
)

// fixture is one origin repository (in-memory, served over the file
// transport for the direct source) plus an in-process proxy transport
// serving a mutable endpoint map — no sockets: real listeners read
// volatile OS state (net.core.somaxconn), which destabilizes
// mutation-test oracles and puts network I/O in the observed inputs.
type fixture struct {
	t    *testing.T
	repo *gittest.Repo

	// endpoints maps "host/path" keys
	// ("proxy.test/example.com/m/@v/v1.0.0.zip") to response bytes;
	// absent keys answer 404. status overrides the answer for a key.
	// hits counts requests per key.
	endpoints map[string][]byte
	status    map[string]int
	hits      map[string]int

	// subtrees maps module paths to their origin subtree; every module
	// path resolves to the fixture repository unless resolveOverride is
	// set.
	subtrees        map[string]string
	resolveOverride func(ctx context.Context, modPath string) (origin.Origin, error)
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		t:         t,
		repo:      gittest.New(t),
		endpoints: map[string][]byte{},
		status:    map[string]int{},
		hits:      map[string]int{},
		subtrees:  map[string]string{},
	}
}

// RoundTrip serves the endpoint map in-process.
func (fx *fixture) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.URL.Host + r.URL.Path
	fx.hits[key]++
	code, body := http.StatusNotFound, []byte(nil)
	if c, ok := fx.status[key]; ok {
		code = c
	} else if b, ok := fx.endpoints[key]; ok {
		code, body = http.StatusOK, b
	}
	return &http.Response{
		StatusCode:    code,
		Status:        http.StatusText(code),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        http.Header{},
		Request:       r,
	}, nil
}

// client builds a Client over the fixture. pbproxy is the PBPROXY
// value, with the literal tokens "proxy" and "alt" substituted by the
// fixture's proxy hosts.
func (fx *fixture) client(pbproxy string) *Client {
	pbproxy = strings.ReplaceAll(pbproxy, "alt", "http://"+altHost)
	pbproxy = strings.ReplaceAll(pbproxy, "proxy", "http://"+proxyHost)
	cfg, err := proxy.ParseConfig(pbproxy, "")
	if err != nil {
		fx.t.Fatal(err)
	}
	return &Client{
		HTTP:    &http.Client{Transport: fx},
		Sources: cfg,
		Cache:   &Cache{FS: memfs.New()},
		Lock:    &lockfile.File{},
		ResolveOrigin: func(ctx context.Context, modPath string) (origin.Origin, error) {
			if fx.resolveOverride != nil {
				return fx.resolveOverride(ctx, modPath)
			}
			return origin.Origin{Repo: "file:///", Subtree: fx.subtrees[modPath]}, nil
		},
		Fetcher: direct.Fetcher{ClientOptions: fx.repo.ClientOptions()},
	}
}

func (fx *fixture) endpoint(modPath string, v, kind, body string) string {
	p := proxyHost + "/" + proxy.Escape(modPath) + "/@v/" + proxy.Escape(v) + "." + kind
	fx.endpoints[p] = []byte(body)
	return p
}

// moduleZip renders a file set as the canonical wire container.
func moduleZip(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var af []archive.File
	for p, body := range files {
		af = append(af, archive.File{Path: p, Body: strings.NewReader(body)})
	}
	slices.SortFunc(af, func(a, b archive.File) int { return strings.Compare(a.Path, b.Path) })
	var buf bytes.Buffer
	digest, err := archive.WriteZip(&buf, af)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), digest
}

// commitFor writes the file set as real git trees plus a commit, and
// returns the commit hash. Used by provenance binding (the archive's
// recomputed tree must match) and the direct source.
func (fx *fixture) commitFor(files map[string]string, when time.Time) plumbing.Hash {
	tree := fx.treeFor(files)
	return fx.repo.CommitTree(tree, "release", when)
}

// treeFor builds nested git trees for a file set, entries in git's sort
// order (directories compare as name + "/").
func (fx *fixture) treeFor(files map[string]string) plumbing.Hash {
	type node struct {
		blobs map[string]string
		dirs  map[string]map[string]string
	}
	n := node{blobs: map[string]string{}, dirs: map[string]map[string]string{}}
	for p, body := range files {
		name, rest, nested := strings.Cut(p, "/")
		if !nested {
			n.blobs[name] = body
			continue
		}
		if n.dirs[name] == nil {
			n.dirs[name] = map[string]string{}
		}
		n.dirs[name][rest] = body
	}
	type entry struct {
		name    string
		sortKey string
		te      object.TreeEntry
	}
	var entries []entry
	for name, body := range n.blobs {
		entries = append(entries, entry{name, name, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: fx.repo.Blob(body)}})
	}
	for name, sub := range n.dirs {
		entries = append(entries, entry{name, name + "/", object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: fx.treeFor(sub)}})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey < entries[j].sortKey })
	tes := make([]object.TreeEntry, len(entries))
	for i, e := range entries {
		tes[i] = e.te
	}
	return fx.repo.Tree(tes...)
}

// gitWhen is the fixed commit time fixtures use.
var gitWhen = time.Unix(1700000000, 0)

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
