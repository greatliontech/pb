package direct

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/testing/gittest"
	"pgregory.net/rapid"
)

const fakeSig = gittest.FakeSignature

// artifactFixture hosts one commit whose tree carries a declared root
// module and a synthesized subtree module at sub/mod (a declared
// subtree module would be a nested module file from the root's
// perspective, which the archive contract forbids), plus one release
// tag of each provenance shape.
type artifactFixture struct {
	*repoFixture
	c    plumbing.Hash
	when time.Time
	mod  string
}

func newArtifactFixture(t failer) *artifactFixture {
	f := &artifactFixture{
		repoFixture: newFixture(t),
		when:        time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
		mod:         "module: example.com/m\n",
	}
	dir := f.Tree(object.TreeEntry{Name: "b.proto", Mode: filemode.Regular, Hash: f.Blob("B\n")})
	mod := f.Tree(object.TreeEntry{Name: "x.proto", Mode: filemode.Regular, Hash: f.Blob("X\n")})
	sub := f.Tree(object.TreeEntry{Name: "mod", Mode: filemode.Dir, Hash: mod})
	// Entries in canonical git order (directories sort with a trailing
	// "/"): the recomputed tree hash must match these exact bytes.
	root := f.Tree(
		object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
		object.TreeEntry{Name: "dir", Mode: filemode.Dir, Hash: dir},
		object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: f.Blob(f.mod)},
		object.TreeEntry{Name: "sub", Mode: filemode.Dir, Hash: sub},
		object.TreeEntry{Name: "tool.sh", Mode: filemode.Executable, Hash: f.Blob("#!/bin/sh\n")},
	)
	f.c = f.CommitTree(root, "c", f.when)
	f.Branch("main", f.c)
	f.Head("main")
	f.SignedTag("v1.0.0", f.c, plumbing.CommitObject, f.when, fakeSig)
	f.AnnotatedTag("v2.0.0", f.c, plumbing.CommitObject, f.when) // unsigned
	f.Tag("v3.0.0", f.c)                                         // lightweight
	f.SignedTag("sub/mod/v1.0.0", f.c, plumbing.CommitObject, f.when, fakeSig)
	f.SignedTag("obj/v1.0.0", root, plumbing.TreeObject, f.when, fakeSig) // signed, names no commit
	return f
}

func TestArchiveRoundTripsProxyVerification(t *testing.T) {
	f := newArtifactFixture(t)
	repo := f.fetch()
	var buf bytes.Buffer
	digest, err := repo.Archive(context.Background(), &buf, f.c.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := archive.VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), digest)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		exec    bool
		content string
	}{
		"a.proto":         {false, "A\n"},
		"dir/b.proto":     {false, "B\n"},
		"pb.yaml":         {false, f.mod},
		"sub/mod/x.proto": {false, "X\n"},
		"tool.sh":         {true, "#!/bin/sh\n"},
	}
	if len(infos) != len(want) {
		t.Fatalf("file set = %+v, want %d members", infos, len(want))
	}
	for _, fi := range infos {
		w, ok := want[fi.Path]
		if !ok || fi.Exec != w.exec {
			t.Fatalf("member %+v not in wanted set %v", fi, want)
		}
		// The served bytes must be the origin blob's bytes: VerifyZip
		// derives Size and SHA256 from the actual members, so this ties
		// content — not just the member set — back to the origin.
		if fi.Size != int64(len(w.content)) || fi.SHA256 != sha256.Sum256([]byte(w.content)) {
			t.Fatalf("member %q content differs from the origin blob", fi.Path)
		}
	}
}

// The subtree archive, the verification pack, and the archive contract
// close the loop: the file set recomputed from the served zip
// tree-binds to the commit and treePath the pack carries — the
// equivalence REQ-proxy-direct-equivalence names.
func TestArchiveTreeBindingEquivalence(t *testing.T) {
	f := newArtifactFixture(t)
	repo := f.fetch()

	pack, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "sub/mod", "sub/mod")
	if err != nil || !ok {
		t.Fatalf("pack ok=%v err=%v", ok, err)
	}
	env, err := proxy.ParseEnvelope(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.GitSignedTags) != 1 {
		t.Fatalf("evidence = %+v", env)
	}
	ev := env.GitSignedTags[0]
	if ev.ObjectFormat != "sha1" || len(ev.TreePath) != 2 {
		t.Fatalf("evidence shape = format %q, %d tree objects; want sha1 with 2", ev.ObjectFormat, len(ev.TreePath))
	}

	var buf bytes.Buffer
	digest, err := repo.Archive(context.Background(), &buf, f.c.String(), "sub/mod")
	if err != nil {
		t.Fatal(err)
	}
	fis, err := archive.VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), digest)
	if err != nil {
		t.Fatal(err)
	}
	// The blob hashes are recomputed from the ZIP'S OWN member bytes,
	// never from fixture literals: if Archive served different content,
	// the recomputed tree hash would differ and the binding below would
	// fail — that is the content half of the equivalence.
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	format := archive.ObjectFormat(ev.ObjectFormat)
	entries := make([]archive.TreeEntry, 0, len(fis))
	for _, fi := range fis {
		if fi.Path != "x.proto" {
			t.Fatalf("subtree file set has %q", fi.Path)
		}
		member, err := zr.Open(fi.Path)
		if err != nil {
			t.Fatal(err)
		}
		served, err := io.ReadAll(member)
		member.Close()
		if err != nil {
			t.Fatal(err)
		}
		blob, err := archive.BlobHash(format, int64(len(served)), bytes.NewReader(served))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, archive.TreeEntry{Path: fi.Path, Exec: fi.Exec, Hash: blob})
	}
	computed, err := archive.TreeHash(format, entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.VerifyTreeBinding(format, ev.Commit, "sub/mod", ev.TreePath, computed); err != nil {
		t.Fatalf("tree binding: %v", err)
	}
}

// A link and a submodule entry under the module root are carried as
// git stores them (REQ-archive-links-carried): the archive holds the
// link's target and the submodule's recorded id, its digest names their
// kinds, its tree hash binds to the origin commit's, and the module's
// files are the regular ones alone.
func TestArchiveCarriesLinks(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	f := newFixture(t)
	sub := plumbing.NewHash(strings.Repeat("ab", 20))
	root := f.Tree(
		object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
		object.TreeEntry{Name: "link.proto", Mode: filemode.Symlink, Hash: f.Blob("a.proto")},
		object.TreeEntry{Name: "vendor", Mode: filemode.Submodule, Hash: sub},
	)
	commit := f.CommitTree(root, "c", when)
	f.Branch("main", commit)
	f.Head("main")
	repo := f.open()
	var buf bytes.Buffer
	digest, err := repo.Archive(context.Background(), &buf, commit.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	infos, err := archive.VerifyZip(bytes.NewReader(data), int64(len(data)), digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 || infos[1].Kind != archive.KindLink || infos[2].Kind != archive.KindSubmodule || !bytes.Equal(infos[2].Submodule, sub.Bytes()) {
		t.Fatalf("verified %+v", infos)
	}
	computed, err := archive.ZipTreeHash(archive.SHA1, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rawCommit, err := repo.rawBody(commit)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.VerifyTreeBinding(archive.SHA1, rawCommit, "", nil, computed); err != nil {
		t.Fatalf("tree binding over a link and a submodule: %v", err)
	}
	files, err := archive.ZipFiles(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(files) != 1 || string(files["a.proto"]) != "A\n" {
		t.Fatalf("the module's files: %v %v", files, err)
	}
}

// A file set that drops a link cannot bind to the tree that holds it
// (REQ-archive-tree-binding): carrying the link is what makes the
// recompute exact.
func TestLinkDroppedCannotBind(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	f := newFixture(t)
	root := f.Tree(
		object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
		object.TreeEntry{Name: "link", Mode: filemode.Symlink, Hash: f.Blob("a.proto")},
	)
	commit := f.CommitTree(root, "c", when)
	f.Branch("main", commit)
	f.Head("main")
	repo := f.open()

	blob, err := archive.BlobHash(archive.SHA1, int64(len("A\n")), strings.NewReader("A\n"))
	if err != nil {
		t.Fatal(err)
	}
	computed, err := archive.TreeHash(archive.SHA1, []archive.TreeEntry{{Path: "a.proto", Hash: blob}})
	if err != nil {
		t.Fatal(err)
	}
	rawCommit, err := repo.rawBody(commit)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.VerifyTreeBinding(archive.SHA1, rawCommit, "", nil, computed); err == nil {
		t.Fatal("a file set without the link bound to the tree holding it")
	}
}

func TestArchiveNestedModuleFails(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	f := newFixture(t)
	nested := f.Tree(object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: f.Blob("module: example.com/n\n")})
	root := f.Tree(
		object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
		object.TreeEntry{Name: "nested", Mode: filemode.Dir, Hash: nested},
	)
	commit := f.CommitTree(root, "c", when)
	f.Branch("main", commit)
	f.Head("main")
	var buf bytes.Buffer
	if _, err := f.open().Archive(context.Background(), &buf, commit.String(), ""); !errors.Is(err, archive.ErrNestedModule) {
		t.Fatalf("err = %v, want archive.ErrNestedModule", err)
	}
}

func TestModuleFileBytes(t *testing.T) {
	f := newArtifactFixture(t)
	repo := f.fetch()
	data, ok, err := repo.ModuleFileBytes(context.Background(), f.c.String(), "")
	if err != nil || !ok || string(data) != f.mod {
		t.Fatalf("root module file = %q ok=%v err=%v, want exact declared bytes", data, ok, err)
	}
	// The subtree module is synthesized: no module file exists, and a
	// proxy answers not-here for its .mod.
	data, ok, err = repo.ModuleFileBytes(context.Background(), f.c.String(), "sub/mod")
	if err != nil || ok || data != nil {
		t.Fatalf("synthesized module file = %q ok=%v err=%v, want absent", data, ok, err)
	}
}

func TestInfoJSONRoundTrips(t *testing.T) {
	v := mustParse(t, "v1.0.0")
	c := origin.Commit{
		Hash: strings.Repeat("ab", 20),
		Time: time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("EET", 2*60*60)),
	}
	body, err := InfoJSON(v, c)
	if err != nil {
		t.Fatal(err)
	}
	// The wire time is RFC 3339 UTC, not the committer's offset.
	if !strings.Contains(string(body), `"time":"2026-07-15T10:00:00Z"`) {
		t.Fatalf("info body = %s", body)
	}
	info, err := proxy.ParseInfo(body)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version.String() != "v1.0.0" || !info.Time.Equal(c.Time) {
		t.Fatalf("round-tripped info = %+v", info)
	}
}

func TestVerificationPackShapes(t *testing.T) {
	f := newArtifactFixture(t)
	repo := f.fetch()

	t.Run("signed tag yields the evidence verbatim", func(t *testing.T) {
		pack, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", "")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		env, err := proxy.ParseEnvelope(pack)
		if err != nil {
			t.Fatal(err)
		}
		if len(env.GitSignedTags) != 1 {
			t.Fatalf("evidence = %+v", env)
		}
		ev := env.GitSignedTags[0]
		if len(ev.TreePath) != 0 {
			t.Fatalf("root module treePath = %d entries", len(ev.TreePath))
		}
		ref, err := f.St.Reference(plumbing.ReferenceName("refs/tags/v1.0.0"))
		if err != nil {
			t.Fatal(err)
		}
		wantTag, err := repo.rawBody(ref.Hash())
		if err != nil {
			t.Fatal(err)
		}
		wantCommit, err := repo.rawBody(f.c)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ev.Tag, wantTag) || !bytes.Equal(ev.Commit, wantCommit) {
			t.Fatal("pack bytes are not the verbatim origin object bodies")
		}
		if !bytes.Contains(ev.Tag, []byte(fakeSig)) {
			t.Fatal("signed tag body does not carry the signature block")
		}
	})
	t.Run("unsigned annotated tag has no evidence", func(t *testing.T) {
		if _, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v2.0.0"), "", ""); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want absent", ok, err)
		}
	})
	t.Run("lightweight tag has no evidence", func(t *testing.T) {
		if _, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v3.0.0"), "", ""); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want absent", ok, err)
		}
	})
	t.Run("pseudo-version has no evidence", func(t *testing.T) {
		v := mustPseudo(t, nil, f.when, f.c.String())
		if _, ok, err := repo.VerificationPack(context.Background(), v, "", ""); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want absent", ok, err)
		}
	})
	t.Run("signed tag naming a non-commit has no evidence", func(t *testing.T) {
		if _, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "obj", "obj"); ok || err != nil {
			t.Fatalf("ok=%v err=%v, want absent", ok, err)
		}
	})
	t.Run("unknown version errors", func(t *testing.T) {
		if _, _, err := repo.VerificationPack(context.Background(), mustParse(t, "v9.9.9"), "", ""); !errors.Is(err, ErrUnknownVersion) {
			t.Fatalf("err = %v, want ErrUnknownVersion", err)
		}
	})
}

// Wherever a link or submodule entry sits under the module root — any
// depth, with or without regular siblings — the archive carries it and
// its tree hash binds to the commit (REQ-archive-links-carried,
// REQ-archive-tree-binding).
func TestLinksCarriedProperty(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	rapid.Check(t, func(rt *rapid.T) {
		f := newFixture(rt)
		mode := rapid.SampledFrom([]filemode.FileMode{filemode.Symlink, filemode.Submodule}).Draw(rt, "mode")
		hash := f.Blob("x")
		if mode == filemode.Submodule {
			hash = plumbing.NewHash(rapid.StringMatching(`[0-9a-f]{40}`).Draw(rt, "id"))
		}
		entries := []object.TreeEntry{{Name: "carried", Mode: mode, Hash: hash}}
		if rapid.Bool().Draw(rt, "sibling") {
			entries = append([]object.TreeEntry{
				{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
			}, entries...)
		}
		tree := f.Tree(entries...)
		depth := rapid.IntRange(0, 3).Draw(rt, "depth")
		for i := depth; i > 0; i-- {
			tree = f.Tree(
				object.TreeEntry{Name: fmt.Sprintf("d%d", i), Mode: filemode.Dir, Hash: tree},
				object.TreeEntry{Name: fmt.Sprintf("f%d.proto", i), Mode: filemode.Regular, Hash: f.Blob(fmt.Sprintf("%d\n", i))},
			)
		}
		c := f.CommitTree(tree, "c", when)
		f.Branch("main", c)
		f.Head("main")
		repo := f.open()
		var buf bytes.Buffer
		digest, err := repo.Archive(context.Background(), &buf, c.String(), "")
		if err != nil {
			rt.Fatal(err)
		}
		data := buf.Bytes()
		if _, err := archive.VerifyZip(bytes.NewReader(data), int64(len(data)), digest); err != nil {
			rt.Fatal(err)
		}
		computed, err := archive.ZipTreeHash(archive.SHA1, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			rt.Fatal(err)
		}
		rawCommit, err := repo.rawBody(c)
		if err != nil {
			rt.Fatal(err)
		}
		if err := archive.VerifyTreeBinding(archive.SHA1, rawCommit, "", nil, computed); err != nil {
			rt.Fatal("the archive's tree does not bind: ", err)
		}
	})
}

// fileSetInfos wires blob metadata into the archive validation: path,
// exec, and — critically — declared size must map through, which is
// what lets an oversized module fail before any content is read
// (REQ-archive-size-limit; a >500 MiB fixture is not buildable, so the
// pure mapping is what makes the wiring observable; the limit itself
// is the archive package's pinned domain).
func TestFileSetInfos(t *testing.T) {
	id := bytes.Repeat([]byte{1}, 20)
	entries := []fileEntry{
		{path: "a.proto", blob: &object.Blob{Size: 7}},
		{path: "tool.sh", exec: true, blob: &object.Blob{Size: archive.MaxTotalSize}},
		{path: "link", kind: archive.KindLink, blob: &object.Blob{Size: 3}},
		{path: "sub", kind: archive.KindSubmodule, submodule: id},
	}
	infos := fileSetInfos(entries)
	want := []archive.FileInfo{
		{Path: "a.proto", Size: 7},
		{Path: "tool.sh", Exec: true, Size: archive.MaxTotalSize},
		{Path: "link", Kind: archive.KindLink, Size: 3},
		{Path: "sub", Kind: archive.KindSubmodule, Submodule: id},
	}
	if !reflect.DeepEqual(infos, want) {
		t.Fatalf("fileSetInfos = %+v, want %+v", infos, want)
	}
	if err := archive.ValidateFileSet(infos); !errors.Is(err, archive.ErrTooLarge) {
		t.Fatalf("declared-oversize err = %v, want ErrTooLarge through the mapping", err)
	}
}

// objectFormatName over both git object formats and the degenerate
// zero hash.
func TestObjectFormatName(t *testing.T) {
	sha1Hash := plumbing.NewHash(strings.Repeat("ab", 20))
	if got, err := objectFormatName(sha1Hash); err != nil || got != "sha1" {
		t.Fatalf("sha1: %q %v", got, err)
	}
	sha256Hash := plumbing.NewHash(strings.Repeat("ab", 32))
	if got, err := objectFormatName(sha256Hash); err != nil || got != "sha256" {
		t.Fatalf("sha256: %q %v", got, err)
	}
}

// Every artifact constructor fails loudly on broken or absent origin
// state instead of serving a partial artifact.
func TestArtifactFailures(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	absent := strings.Repeat("cd", 20)

	build := func(t *testing.T) (*repoFixture, plumbing.Hash) {
		f := newFixture(t)
		root := f.Tree(
			object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
			object.TreeEntry{Name: "dir", Mode: filemode.Dir, Hash: f.Tree(
				object.TreeEntry{Name: "b.proto", Mode: filemode.Regular, Hash: f.Blob("B\n")},
			)},
		)
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		return f, c
	}

	t.Run("absent commit", func(t *testing.T) {
		f, _ := build(t)
		repo := f.open()
		var buf bytes.Buffer
		if _, err := repo.Archive(context.Background(), &buf, absent, ""); err == nil || !strings.Contains(err.Error(), "reading commit") {
			t.Fatalf("Archive err = %v", err)
		}
		if _, _, err := repo.ModuleFileBytes(context.Background(), absent, ""); err == nil || !strings.Contains(err.Error(), "reading commit") {
			t.Fatalf("ModuleFileBytes err = %v", err)
		}
	})
	t.Run("missing subtree segment", func(t *testing.T) {
		f, c := build(t)
		repo := f.open()
		var buf bytes.Buffer
		if _, err := repo.Archive(context.Background(), &buf, c.String(), "nope"); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("Archive err = %v", err)
		}
		// The absent module root is classifiable: consumers read it as
		// "not a declared module at this commit", not as a storage fault.
		if _, _, err := repo.ModuleFileBytes(context.Background(), c.String(), "nope"); !errors.Is(err, ErrNoModuleRoot) {
			t.Fatalf("ModuleFileBytes err = %v, want ErrNoModuleRoot", err)
		}
	})
	t.Run("subtree segment is a file", func(t *testing.T) {
		f, c := build(t)
		var buf bytes.Buffer
		if _, err := f.open().Archive(context.Background(), &buf, c.String(), "a.proto"); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("dangling tree of the commit", func(t *testing.T) {
		f := newFixture(t)
		c := f.CommitTree(plumbing.NewHash(absent), "c", when)
		f.Branch("main", c)
		f.Head("main")
		var buf bytes.Buffer
		if _, err := f.open().Archive(context.Background(), &buf, c.String(), ""); err == nil || !strings.Contains(err.Error(), "reading root tree") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("dangling directory entry", func(t *testing.T) {
		f := newFixture(t)
		root := f.Tree(object.TreeEntry{Name: "d", Mode: filemode.Dir, Hash: plumbing.NewHash(absent)})
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		repo := f.open()
		var buf bytes.Buffer
		if _, err := repo.Archive(context.Background(), &buf, c.String(), ""); err == nil || !strings.Contains(err.Error(), "reading tree") {
			t.Fatalf("fileSet walk err = %v", err)
		}
		if _, err := repo.Archive(context.Background(), &buf, c.String(), "d"); err == nil || !strings.Contains(err.Error(), "reading tree") {
			t.Fatalf("moduleRoot walk err = %v", err)
		}
	})
	t.Run("dangling blob entry", func(t *testing.T) {
		f := newFixture(t)
		root := f.Tree(object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: plumbing.NewHash(absent)})
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		var buf bytes.Buffer
		if _, err := f.open().Archive(context.Background(), &buf, c.String(), ""); err == nil || !strings.Contains(err.Error(), "reading blob") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nested module file blocks module file serving too", func(t *testing.T) {
		// A version whose file set the archive contract rejects has no
		// artifacts at all: the root module file must not be served
		// just because the nesting sits elsewhere in the tree.
		f := newFixture(t)
		nested := f.Tree(object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: f.Blob("module: example.com/n\n")})
		root := f.Tree(
			object.TreeEntry{Name: "nested", Mode: filemode.Dir, Hash: nested},
			object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: f.Blob("module: example.com/m\n")},
		)
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		if _, _, err := f.open().ModuleFileBytes(context.Background(), c.String(), ""); !errors.Is(err, archive.ErrNestedModule) {
			t.Fatalf("err = %v, want archive.ErrNestedModule", err)
		}
	})
	t.Run("a link named like the module file is no module file", func(t *testing.T) {
		f := newFixture(t)
		root := f.Tree(
			object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: f.Blob("A\n")},
			object.TreeEntry{Name: "pb.yaml", Mode: filemode.Symlink, Hash: f.Blob("../pb.yaml")},
		)
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		if b, ok, err := f.open().ModuleFileBytes(context.Background(), c.String(), ""); err != nil || ok || b != nil {
			t.Fatalf("a link read as the module file: %q %v %v", b, ok, err)
		}
	})
	t.Run("nonstandard wire mode canonicalizes to regular", func(t *testing.T) {
		// go-git's encoder refuses nonstandard modes, so the tree is
		// written as raw bytes; the decoder canonicalizes 100600 (no
		// exec bits) to a regular file, which the file set carries
		// non-executable — the walk never sees a non-canonical mode.
		f := newFixture(t)
		blob := f.Blob("x\n")
		raw := append([]byte("100600 odd\x00"), blob.Bytes()...)
		tree := f.CorruptObject(plumbing.TreeObject, string(raw))
		c := f.CommitTree(tree, "c", when)
		f.Branch("main", c)
		f.Head("main")
		var buf bytes.Buffer
		digest, err := f.open().Archive(context.Background(), &buf, c.String(), "")
		if err != nil {
			t.Fatal(err)
		}
		infos, err := archive.VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), digest)
		if err != nil || len(infos) != 1 || infos[0].Path != "odd" || infos[0].Exec {
			t.Fatalf("infos=%+v err=%v, want one regular non-exec member", infos, err)
		}
	})
}

func TestVerificationPackFailures(t *testing.T) {
	when := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)

	t.Run("symbolic tag ref resolves through", func(t *testing.T) {
		f := newArtifactFixture(t)
		f.Symref("refs/tags/v9.0.0", "refs/tags/v1.0.0")
		pack, ok, err := f.open().VerificationPack(context.Background(), mustParse(t, "v9.0.0"), "", "")
		if err != nil || !ok || len(pack) == 0 {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
	t.Run("sha256-header signature counts as signed", func(t *testing.T) {
		f := newFixture(t)
		c := f.Commit("c", when)
		f.Branch("main", c)
		f.Head("main")
		f.SignedTagSHA256("v1.0.0", c, plumbing.CommitObject, when, fakeSig)
		_, ok, err := f.open().VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", "")
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v, want evidence", ok, err)
		}
	})
	t.Run("corrupt tag object fails loudly", func(t *testing.T) {
		f := newFixture(t)
		c := f.Commit("c", when)
		f.Branch("main", c)
		f.Head("main")
		f.Ref("refs/tags/v1.0.0", f.CorruptObject(plumbing.TagObject, "not a decodable tag object"))
		if _, _, err := f.open().VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", ""); err == nil || !strings.Contains(err.Error(), "reading tag object") {
			t.Fatalf("err = %v, want a loud tag decode failure", err)
		}
	})
	t.Run("invalid file set withholds the pack too", func(t *testing.T) {
		// A version whose file set the archive contract rejects has no
		// artifacts at all — the verification pack included.
		f := newFixture(t)
		nested := f.Tree(object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: f.Blob("module: example.com/n\n")})
		root := f.Tree(object.TreeEntry{Name: "bad", Mode: filemode.Dir, Hash: nested})
		c := f.CommitTree(root, "c", when)
		f.Branch("main", c)
		f.Head("main")
		f.SignedTag("v1.0.0", c, plumbing.CommitObject, when, fakeSig)
		if _, _, err := f.open().VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", ""); !errors.Is(err, archive.ErrNestedModule) {
			t.Fatalf("err = %v, want archive.ErrNestedModule", err)
		}
	})
	t.Run("missing module root fails loudly", func(t *testing.T) {
		f := newFixture(t)
		c := f.Commit("c", when)
		f.Branch("main", c)
		f.Head("main")
		f.SignedTag("nope/v1.0.0", c, plumbing.CommitObject, when, fakeSig)
		if _, _, err := f.open().VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "nope", "nope"); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v, want a module-root failure", err)
		}
	})
	t.Run("signed tag referencing an absent commit fails loudly", func(t *testing.T) {
		f := newFixture(t)
		c := f.Commit("c", when)
		f.Branch("main", c)
		f.Head("main")
		f.SignedTag("v1.0.0", plumbing.NewHash(strings.Repeat("cd", 20)), plumbing.CommitObject, when, fakeSig)
		if _, _, err := f.open().VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", ""); err == nil || !strings.Contains(err.Error(), "reading commit") {
			t.Fatalf("err = %v, want a loud absent-commit failure", err)
		}
	})
}

// A synthesized subtree's pack carries the repository's signed tag
// with the tree path to the subtree (REQ-prov-tag-binding): the tag
// looked up in the namespace that named the version, the tree walked
// to the module root; the subtree's own namespace names no such
// version.
func TestVerificationPackSynthesizedSubtree(t *testing.T) {
	f := newArtifactFixture(t)
	repo := f.fetch()
	pack, ok, err := repo.VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "", "dir")
	if err != nil || !ok {
		t.Fatalf("pack ok=%v err=%v", ok, err)
	}
	env, err := proxy.ParseEnvelope(pack)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.GitSignedTags) != 1 || len(env.GitSignedTags[0].TreePath) != 1 {
		t.Fatalf("evidence = %+v", env)
	}
	if _, _, err := repo.VerificationPack(context.Background(), mustParse(t, "v1.0.0"), "dir", "dir"); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("the subtree's own namespace: %v", err)
	}
}
