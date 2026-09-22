// Package fetchtest builds fetch clients over in-process fixtures for
// tests: an origin repository served through the gittest file transport
// for the direct source, and a socket-free proxy transport serving a
// mutable endpoint map — real listeners read volatile OS state and put
// network I/O in mutation-test oracles, so nothing here opens one.
package fetchtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/testing/gittest"
)

// ProxyHost and AltHost are the fixture's in-process proxy hosts: the
// literal tokens "proxy" and "alt" in a client's source list resolve
// to them.
const (
	ProxyHost = "proxy.test"
	AltHost   = "alt.test"
)

// GitWhen is the fixed commit time fixtures use.
var GitWhen = time.Unix(1700000000, 0)

// Fixture is one origin repository plus the in-process proxy transport
// and the client wiring under test.
type Fixture struct {
	T    *testing.T
	Repo *gittest.Repo

	// Endpoints maps "host/path" keys
	// ("proxy.test/example.com/m/@v/v1.0.0.zip") to response bytes;
	// absent keys answer 404. Status overrides the answer for a key.
	// Hits counts requests per key; Authorization keeps the last
	// request's authorization header per key.
	Endpoints     map[string][]byte
	Status        map[string]int
	Hits          map[string]int
	Authorization map[string]string

	// Subtrees maps module paths to their origin subtree; every module
	// path resolves to the fixture repository unless ResolveOverride is
	// set.
	Subtrees        map[string]string
	ResolveOverride func(ctx context.Context, modPath string) (origin.Origin, error)
}

// New builds an empty fixture.
func New(t *testing.T) *Fixture {
	return &Fixture{
		T:             t,
		Repo:          gittest.New(t),
		Endpoints:     map[string][]byte{},
		Status:        map[string]int{},
		Hits:          map[string]int{},
		Authorization: map[string]string{},
		Subtrees:      map[string]string{},
	}
}

// RoundTrip serves the endpoint map in-process.
func (fx *Fixture) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.URL.Host + r.URL.Path
	fx.Hits[key]++
	fx.Authorization[key] = r.Header.Get("Authorization")
	code, body := http.StatusNotFound, []byte(nil)
	if c, ok := fx.Status[key]; ok {
		code = c
	} else if b, ok := fx.Endpoints[key]; ok {
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

// Sources parses a proxy setting value with the literal tokens "proxy" and
// "alt" substituted by the fixture's proxy hosts.
func (fx *Fixture) Sources(pbproxy string) proxy.Config {
	pbproxy = strings.ReplaceAll(pbproxy, "alt", "http://"+AltHost)
	pbproxy = strings.ReplaceAll(pbproxy, "proxy", "http://"+ProxyHost)
	sources, err := proxy.ParseSources(pbproxy)
	if err != nil {
		fx.T.Fatal(err)
	}
	return proxy.Config{Sources: sources}
}

// HTTPClient is an http client over the fixture's in-process transport.
func (fx *Fixture) HTTPClient() *http.Client { return &http.Client{Transport: fx} }

// Resolve is the fixture's origin resolver: every path maps to the
// fixture repository unless ResolveOverride is set.
func (fx *Fixture) Resolve(ctx context.Context, modPath string) (origin.Origin, error) {
	if fx.ResolveOverride != nil {
		return fx.ResolveOverride(ctx, modPath)
	}
	return origin.Origin{Repo: "file:///", Subtree: fx.Subtrees[modPath]}, nil
}

// Fetcher is the direct-source fetcher over the fixture's file
// transport.
func (fx *Fixture) Fetcher() direct.Fetcher {
	return direct.Fetcher{ClientOptions: fx.Repo.ClientOptions()}
}

// Endpoint registers a version-addressed artifact on the primary proxy
// host and returns its hit key.
func (fx *Fixture) Endpoint(modPath string, v, kind, body string) string {
	p := ProxyHost + "/" + proxy.Escape(modPath) + "/@v/" + proxy.Escape(v) + "." + kind
	fx.Endpoints[p] = []byte(body)
	return p
}

// ModuleZip renders a file set as the canonical wire container.
func ModuleZip(t *testing.T, files map[string]string) ([]byte, string) {
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

// CommitFor writes the file set as real git trees plus a commit in the
// fixture repository and returns the commit hash.
func (fx *Fixture) CommitFor(files map[string]string, when time.Time) plumbing.Hash {
	tree := fx.TreeFor(fx.Repo, files)
	return fx.Repo.CommitTree(tree, "release", when)
}

// TreeFor builds nested git trees for a file set in the given
// repository, entries in git's sort order (directories compare as
// name + "/").
func (fx *Fixture) TreeFor(r *gittest.Repo, files map[string]string) plumbing.Hash {
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
		entries = append(entries, entry{name, name, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: r.Blob(body)}})
	}
	for name, sub := range n.dirs {
		entries = append(entries, entry{name, name + "/", object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: fx.TreeFor(r, sub)}})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey < entries[j].sortKey })
	tes := make([]object.TreeEntry, len(entries))
	for i, e := range entries {
		tes[i] = e.te
	}
	return r.Tree(tes...)
}

// ErrInjected is the sentinel every ErrFS fault returns.
var ErrInjected = errors.New("injected storage fault")

// ErrFS injects storage faults: each flag fails one operation class,
// suffix variants scope the fault to matching names, and PutFailAfter
// fails TempFile only after that many successes.
type ErrFS struct {
	billy.Filesystem
	FailOpen       bool
	FailOpenSuffix string
	FailStatSuffix string
	FailReadBody   bool
	FailMkdirAll   bool
	FailTempFile   bool
	FailRename     bool
	FailRenameSfx  string
	FailWrite      bool
	FailClose      bool
	PutFailAfter   int // -1 = never; N fails the (N+1)th TempFile
	tempFiles      int
}

// FailStat fails Stat for names with the suffix.
func (e *ErrFS) Stat(name string) (fs.FileInfo, error) {
	if e.FailStatSuffix != "" && strings.HasSuffix(name, e.FailStatSuffix) {
		return nil, ErrInjected
	}
	return e.Filesystem.Stat(name)
}

func (e *ErrFS) Open(name string) (billy.File, error) {
	if e.FailOpen || (e.FailOpenSuffix != "" && strings.HasSuffix(name, e.FailOpenSuffix)) {
		return nil, ErrInjected
	}
	f, err := e.Filesystem.Open(name)
	if err != nil {
		return nil, err
	}
	return &errFile{File: f, failRead: e.FailReadBody}, nil
}

func (e *ErrFS) MkdirAll(name string, perm fs.FileMode) error {
	if e.FailMkdirAll {
		return ErrInjected
	}
	return e.Filesystem.MkdirAll(name, perm)
}

func (e *ErrFS) Rename(from, to string) error {
	if e.FailRename || (e.FailRenameSfx != "" && strings.HasSuffix(to, e.FailRenameSfx)) {
		return ErrInjected
	}
	return e.Filesystem.Rename(from, to)
}

func (e *ErrFS) TempFile(dir, prefix string) (billy.File, error) {
	if e.FailTempFile {
		return nil, ErrInjected
	}
	if e.PutFailAfter >= 0 {
		if e.tempFiles >= e.PutFailAfter {
			return nil, ErrInjected
		}
		e.tempFiles++
	}
	f, err := e.Filesystem.TempFile(dir, prefix)
	if err != nil {
		return nil, err
	}
	return &errFile{File: f, failWrite: e.FailWrite, failClose: e.FailClose}, nil
}

type errFile struct {
	billy.File
	failWrite bool
	failClose bool
	failRead  bool
}

func (f *errFile) Read(p []byte) (int, error) {
	if f.failRead {
		return 0, ErrInjected
	}
	return f.File.Read(p)
}

func (f *errFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, ErrInjected
	}
	return f.File.Write(p)
}

func (f *errFile) Close() error {
	if f.failClose {
		return ErrInjected
	}
	return f.File.Close()
}
