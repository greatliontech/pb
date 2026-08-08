package archive

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The golden fixture, hashed by real git in both object formats:
//
//	pb.yaml            "module: example.com/m\n"
//	proto/v1/svc.proto "syntax = \"proto3\";"
//	tools/gen.sh       "#!/bin/sh\n"  (executable)
//	a.b                "x"
//	a/inner.txt        "inner"
//	a0                 "zed"
//
// The a.b / a / a0 trio pins git's sort rule (directories compare as
// name+"/": "a.b" < "a/" < "a0").
func goldenTreeEntries(t testing.TB, f ObjectFormat) []TreeEntry {
	t.Helper()
	blob := func(body string) []byte {
		h, err := BlobHash(f, int64(len(body)), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	return []TreeEntry{
		{Path: "pb.yaml", Blob: blob("module: example.com/m\n")},
		{Path: "proto/v1/svc.proto", Blob: blob("syntax = \"proto3\";")},
		{Path: "tools/gen.sh", Exec: true, Blob: blob("#!/bin/sh\n")},
		{Path: "a.b", Blob: blob("x")},
		{Path: "a/inner.txt", Blob: blob("inner")},
		{Path: "a0", Blob: blob("zed")},
	}
}

var goldenTrees = map[ObjectFormat]struct {
	root, protoTree, protoV1Tree, blobPbyaml, blobGensh string
}{
	SHA1: {
		root:        "2d94d4ee3800047f3d9f6a1e2f4fe6a0c3b2195c",
		protoTree:   "db3db7f7d39ce641b140959b1bf3fa34dc53b08a",
		protoV1Tree: "7c3593202e4dcc2d84e3cf9548351c9e157f8df6",
		blobPbyaml:  "d5d130577f12841ccb799170e8b1d9e93d778929",
		blobGensh:   "1a2485251c33a70432394c93fb89330ef214bfc9",
	},
	SHA256: {
		root:        "49c1bbe9941d87578113859af0dcf039b4668ab1a0d9e822e3d6466c183f1e7f",
		protoTree:   "ad268752ab8080a80945ac82233af5da3de32f2f3e36bb997037032d42f6d5ed",
		protoV1Tree: "eae024093c6312d98a131ce3edb59d8d855b7dd1858b6667918e599da61a003c",
		blobPbyaml:  "287bd391209d19e0f9aefc445e2249689061ebecfbdcfadc20a5676490223ae3",
		blobGensh:   "1249034e3cf9007362d695b09b1fbdb4c578903bf10b665749b94743f8177ce1",
	},
}

func TestTreeHashGolden(t *testing.T) {
	for f, want := range goldenTrees {
		entries := goldenTreeEntries(t, f)

		if h, _ := BlobHash(f, int64(len("module: example.com/m\n")), strings.NewReader("module: example.com/m\n")); hex.EncodeToString(h) != want.blobPbyaml {
			t.Errorf("%s: pb.yaml blob = %x, want %s", f, h, want.blobPbyaml)
		}
		if h, _ := BlobHash(f, int64(len("#!/bin/sh\n")), strings.NewReader("#!/bin/sh\n")); hex.EncodeToString(h) != want.blobGensh {
			t.Errorf("%s: gen.sh blob = %x, want %s", f, h, want.blobGensh)
		}

		root, err := TreeHash(f, entries)
		if err != nil {
			t.Fatalf("%s: TreeHash: %v", f, err)
		}
		if hex.EncodeToString(root) != want.root {
			t.Errorf("%s: root tree = %x, want %s (git sort rule violated?)", f, root, want.root)
		}

		// Subtree hashes: strip the prefix and hash the remainder.
		var protoOnly []TreeEntry
		for _, e := range entries {
			if rest, ok := strings.CutPrefix(e.Path, "proto/"); ok {
				protoOnly = append(protoOnly, TreeEntry{Path: rest, Exec: e.Exec, Blob: e.Blob})
			}
		}
		sub, err := TreeHash(f, protoOnly)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(sub) != want.protoTree {
			t.Errorf("%s: proto subtree = %x, want %s", f, sub, want.protoTree)
		}
	}
}

func TestTreeHashEmpty(t *testing.T) {
	// Git's well-known empty tree.
	h, err := TreeHash(SHA1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(h) != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Fatalf("empty tree = %x", h)
	}
}

// Compositional property: for any generated file set, the root tree's
// entry hash for a directory equals the TreeHash of that directory's
// entries with the prefix stripped — the recursion is self-consistent, so
// subtree binding walks agree with whole-tree hashing.
func TestTreeHashCompositionProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		f := rapid.SampledFrom([]ObjectFormat{SHA1, SHA256}).Draw(t, "format")
		n := rapid.IntRange(1, 12).Draw(t, "n")
		entries := make([]TreeEntry, 0, n+1)
		var subEntries []TreeEntry
		for i := range n {
			body := rapid.StringN(0, 32, -1).Draw(t, "body")
			blob, err := BlobHash(f, int64(len(body)), strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			exec := rapid.Bool().Draw(t, "exec")
			rel := fmt.Sprintf("f%d.proto", i)
			entries = append(entries, TreeEntry{Path: "sub/" + rel, Exec: exec, Blob: blob})
			subEntries = append(subEntries, TreeEntry{Path: rel, Exec: exec, Blob: blob})
		}
		// One root-level file so the root tree has mixed entry kinds.
		rootBlob, err := BlobHash(f, 1, strings.NewReader("r"))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, TreeEntry{Path: "root.txt", Blob: rootBlob})

		root, err := TreeHash(f, entries)
		if err != nil {
			t.Fatal(err)
		}
		subWant, err := TreeHash(f, subEntries)
		if err != nil {
			t.Fatal(err)
		}
		// Parse the root tree we would encode by re-deriving it: rebuild via
		// dirHash of the two known entries and confirm the "sub" entry
		// carries subWant.
		size, _ := f.Size()
		var body bytes.Buffer
		body.WriteString("100644 root.txt")
		body.WriteByte(0)
		body.Write(rootBlob[:size])
		body.WriteString("40000 sub")
		body.WriteByte(0)
		body.Write(subWant[:size])
		manual, err := ObjectHash(f, "tree", body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(root, manual) {
			t.Fatalf("root tree does not compose from subtree hash")
		}
	})
}

const goldenCommitSHA1 = "tree 2d94d4ee3800047f3d9f6a1e2f4fe6a0c3b2195c\n" +
	"author t <t@t> 1767225600 +0000\n" +
	"committer t <t@t> 1767225600 +0000\n" +
	"\n" +
	"fix\n"

func TestCommitTree(t *testing.T) {
	got, err := CommitTree(SHA1, []byte(goldenCommitSHA1))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != "2d94d4ee3800047f3d9f6a1e2f4fe6a0c3b2195c" {
		t.Fatalf("commit tree = %x", got)
	}
	bad := []string{
		"",
		"author t <t@t> 1 +0000\n",
		"tree zzzz\n",
		"tree 2d94d4ee3800047f3d9f6a1e2f4fe6a0c3b2195c00\n", // too long
		"tree 2d94d4ee3800047f3d9f6a1e2f4fe6a0c3b2195g\n",   // not hex, right length
	}
	for _, c := range bad {
		if _, err := CommitTree(SHA1, []byte(c)); !errors.Is(err, ErrObjectInvalid) {
			t.Errorf("CommitTree(%q): err = %v, want ErrObjectInvalid", c, err)
		}
	}
	// SHA-256 commits carry longer hashes; an SHA-1-length header is invalid.
	if _, err := CommitTree(SHA256, []byte(goldenCommitSHA1)); !errors.Is(err, ErrObjectInvalid) {
		t.Errorf("sha1 commit under sha256 format accepted: %v", err)
	}
}

// rawTree encodes a tree object body from (mode, name, hexhash) triples in
// the given order — a test-side encoder for constructing binding fixtures
// and malformed objects.
func rawTree(t testing.TB, triples ...[3]string) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, tr := range triples {
		raw, err := hex.DecodeString(tr[2])
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(tr[0])
		b.WriteByte(' ')
		b.WriteString(tr[1])
		b.WriteByte(0)
		b.Write(raw)
	}
	return b.Bytes()
}

func TestVerifyTreeBinding(t *testing.T) {
	f := SHA1
	entries := goldenTreeEntries(t, f)
	commit := []byte(goldenCommitSHA1)

	// Repository-root module: recomputed root must equal the commit's tree.
	root, err := TreeHash(f, entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTreeBinding(f, commit, "", nil, root); err != nil {
		t.Fatalf("root binding: %v", err)
	}
	// A wrong recomputed hash is rejected.
	wrong := bytes.Clone(root)
	wrong[0] ^= 0xff
	if err := VerifyTreeBinding(f, commit, "", nil, wrong); !errors.Is(err, ErrTreeMismatch) {
		t.Fatalf("wrong root: err = %v, want ErrTreeMismatch", err)
	}

	// Subtree module proto/v1: the walk needs the root tree and proto tree
	// objects. Reconstruct them exactly as git stores them.
	g := goldenTrees[f]
	blob := func(body string) string {
		h, _ := BlobHash(f, int64(len(body)), strings.NewReader(body))
		return hex.EncodeToString(h)
	}
	aTree, err := TreeHash(f, []TreeEntry{{Path: "inner.txt", Blob: mustHex(t, blob("inner"))}})
	if err != nil {
		t.Fatal(err)
	}
	toolsTree, err := TreeHash(f, []TreeEntry{{Path: "gen.sh", Exec: true, Blob: mustHex(t, blob("#!/bin/sh\n"))}})
	if err != nil {
		t.Fatal(err)
	}
	rootObj := rawTree(t,
		[3]string{"100644", "a.b", blob("x")},
		[3]string{"40000", "a", hex.EncodeToString(aTree)},
		[3]string{"100644", "a0", blob("zed")},
		[3]string{"100644", "pb.yaml", g.blobPbyaml},
		[3]string{"40000", "proto", g.protoTree},
		[3]string{"40000", "tools", hex.EncodeToString(toolsTree)},
	)
	protoObj := rawTree(t, [3]string{"40000", "v1", g.protoV1Tree})

	var v1Entries []TreeEntry
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Path, "proto/v1/"); ok {
			v1Entries = append(v1Entries, TreeEntry{Path: rest, Exec: e.Exec, Blob: e.Blob})
		}
	}
	v1, err := TreeHash(f, v1Entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTreeBinding(f, commit, "proto/v1", [][]byte{rootObj, protoObj}, v1); err != nil {
		t.Fatalf("subtree binding: %v", err)
	}

	cases := []struct {
		name    string
		subtree string
		path    [][]byte
		hash    []byte
	}{
		{"wrong subtree hash", "proto/v1", [][]byte{rootObj, protoObj}, root},
		{"path count mismatch", "proto/v1", [][]byte{rootObj}, v1},
		{"segment not a directory", "pb.yaml", [][]byte{rootObj}, v1},
		{"missing segment", "nope", [][]byte{rootObj}, v1},
		{"tampered tree object", "proto/v1", [][]byte{append(bytes.Clone(rootObj), 'x'), protoObj}, v1},
	}
	for _, tc := range cases {
		err := VerifyTreeBinding(f, commit, tc.subtree, tc.path, tc.hash)
		if !errors.Is(err, ErrTreeMismatch) && !errors.Is(err, ErrObjectInvalid) {
			t.Errorf("%s: err = %v, want ErrTreeMismatch/ErrObjectInvalid", tc.name, err)
		}
	}
}

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBlobHashSizeMismatch(t *testing.T) {
	if _, err := BlobHash(SHA1, 5, strings.NewReader("xy")); !errors.Is(err, ErrObjectInvalid) {
		t.Fatalf("short content: err = %v, want ErrObjectInvalid", err)
	}
	if _, err := BlobHash(SHA1, 5, failingReader{}); err == nil || strings.Contains(err.Error(), "declared") {
		t.Fatalf("failing reader: err = %v, want the read error, not a size mismatch", err)
	}
	// Longer content than declared: only `size` bytes are read and hashed —
	// the declared size is part of the hashed framing, so lying about it
	// changes the hash rather than crashing.
	h5, err := BlobHash(SHA1, 2, strings.NewReader("xyZZZ"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := BlobHash(SHA1, 2, strings.NewReader("xy"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h5, h2) {
		t.Fatalf("prefix read differs from exact read")
	}
}

func TestTreeHashRejectsBadBlobLength(t *testing.T) {
	if _, err := TreeHash(SHA256, []TreeEntry{{Path: "a", Blob: make([]byte, 20)}}); !errors.Is(err, ErrObjectInvalid) {
		t.Fatalf("sha1-length blob under sha256: err = %v, want ErrObjectInvalid", err)
	}
	if _, err := TreeHash("md5", nil); !errors.Is(err, ErrObjectInvalid) {
		t.Fatalf("unknown format: err = %v", err)
	}
}

// FuzzTreeEntries drives the raw tree-object parser: never a panic, and
// every parse that succeeds re-encodes to the identical bytes (the parser
// accepts nothing it cannot faithfully represent).
func FuzzTreeEntries(f *testing.F) {
	var seed bytes.Buffer
	for _, e := range []struct{ mode, name, h string }{
		{"100644", "a", strings.Repeat("11", 20)},
		{"40000", "d", strings.Repeat("22", 20)},
	} {
		raw, _ := hex.DecodeString(e.h)
		seed.WriteString(e.mode)
		seed.WriteByte(' ')
		seed.WriteString(e.name)
		seed.WriteByte(0)
		seed.Write(raw)
	}
	f.Add(seed.Bytes())
	f.Add([]byte{})
	f.Add([]byte("100644 x"))
	f.Fuzz(func(t *testing.T, b []byte) {
		names, modes, hashes, err := treeEntries(SHA1, b)
		if err != nil {
			return
		}
		var re bytes.Buffer
		for i := range names {
			re.WriteString(modes[i])
			re.WriteByte(' ')
			re.WriteString(names[i])
			re.WriteByte(0)
			re.Write(hashes[i])
		}
		if !bytes.Equal(re.Bytes(), b) {
			t.Fatalf("parsed form does not re-encode to input")
		}
	})
}

// FuzzCommitTree: never a panic; success implies the returned hash re-hex
// matches the header line.
func FuzzCommitTree(f *testing.F) {
	f.Add([]byte(goldenCommitSHA1))
	f.Add([]byte("tree \n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		raw, err := CommitTree(SHA1, b)
		if err != nil {
			return
		}
		want := "tree " + hex.EncodeToString(raw)
		if !strings.HasPrefix(string(b), want) {
			t.Fatalf("returned hash does not match header")
		}
	})
}

// Every entry point rejects an unknown object format with ErrObjectInvalid.
func TestUnknownObjectFormat(t *testing.T) {
	const bad ObjectFormat = "md5"
	// The unknown-format rejection must come from the format guard itself
	// (its message names the format), not from a downstream parse failure.
	wantMsg := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrObjectInvalid) || !strings.Contains(err.Error(), "unknown object format") {
			t.Errorf("%s: err = %v, want unknown-object-format rejection", name, err)
		}
	}
	_, err := ObjectHash(bad, "blob", nil)
	wantMsg("ObjectHash", err)
	_, err = BlobHash(bad, 0, strings.NewReader(""))
	wantMsg("BlobHash", err)
	_, err = CommitTree(bad, []byte(goldenCommitSHA1))
	wantMsg("CommitTree", err)
	_, _, _, err = treeEntries(bad, nil)
	wantMsg("treeEntries", err)
	wantMsg("VerifyTreeBinding", VerifyTreeBinding(bad, []byte(goldenCommitSHA1), "", nil, nil))
	_, err = bad.Size()
	wantMsg("Size", err)
	_, err = TreeHash(bad, nil)
	wantMsg("TreeHash", err)
}

// Malformed raw tree objects are rejected with the branch-specific message.
func TestTreeEntriesMalformed(t *testing.T) {
	valid20 := strings.Repeat("\x11", 20)
	cases := []struct {
		name, msg string
		raw       []byte
	}{
		{"no space", "missing mode", []byte("nospace")},
		{"empty mode", "missing mode", []byte(" name\x00" + valid20)},
		{"non-octal mode", "not octal", []byte("10064z name\x00" + valid20)},
		{"digit eight mode", "not octal", []byte("100648 name\x00" + valid20)},
		{"no nul", "missing name", []byte("100644 name-without-nul")},
		{"empty name", "missing name", []byte("100644 \x00" + valid20)},
		{"truncated hash", "truncated", []byte("100644 n\x00" + strings.Repeat("\x11", 19))},
		{"dangling trailing byte", "missing mode", []byte("100644 n\x00" + strings.Repeat("\x11", 20) + "x")},
		{"mode overflows 32 bits", "not octal", []byte("77777777777 n\x00" + strings.Repeat("\x11", 20))},
	}
	for _, tc := range cases {
		_, _, _, err := treeEntries(SHA1, tc.raw)
		if !errors.Is(err, ErrObjectInvalid) {
			t.Errorf("%s: err = %v, want ErrObjectInvalid", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err %q does not name %q", tc.name, err, tc.msg)
		}
	}
	// Single-character modes and names sit exactly on the parser's
	// boundaries and are valid.
	names, modes, _, err := treeEntries(SHA1, []byte("4 x\x00"+valid20))
	if err != nil || len(names) != 1 || names[0] != "x" || modes[0] != "4" {
		t.Fatalf("single-char mode/name: %v %v %v", names, modes, err)
	}
}

// Binding failure cases carry their branch-specific error classes, so a
// wrong branch cannot masquerade as the right rejection.
func TestVerifyTreeBindingErrorClasses(t *testing.T) {
	f := SHA1
	entries := goldenTreeEntries(t, f)
	commit := []byte(goldenCommitSHA1)
	g := goldenTrees[f]
	blob := func(body string) string {
		h, _ := BlobHash(f, int64(len(body)), strings.NewReader(body))
		return hex.EncodeToString(h)
	}
	aTree, _ := TreeHash(f, []TreeEntry{{Path: "inner.txt", Blob: mustHex(t, blob("inner"))}})
	toolsTree, _ := TreeHash(f, []TreeEntry{{Path: "gen.sh", Exec: true, Blob: mustHex(t, blob("#!/bin/sh\n"))}})
	rootObj := rawTree(t,
		[3]string{"100644", "a.b", blob("x")},
		[3]string{"40000", "a", hex.EncodeToString(aTree)},
		[3]string{"100644", "a0", blob("zed")},
		[3]string{"100644", "pb.yaml", g.blobPbyaml},
		[3]string{"40000", "proto", g.protoTree},
		[3]string{"40000", "tools", hex.EncodeToString(toolsTree)},
	)
	protoObj := rawTree(t, [3]string{"40000", "v1", g.protoV1Tree})
	var v1Entries []TreeEntry
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Path, "proto/v1/"); ok {
			v1Entries = append(v1Entries, TreeEntry{Path: rest, Exec: e.Exec, Blob: e.Blob})
		}
	}
	v1, _ := TreeHash(f, v1Entries)

	// A malformed commit surfaces CommitTree's rejection.
	if err := VerifyTreeBinding(f, []byte("garbage"), "", nil, v1); !errors.Is(err, ErrObjectInvalid) {
		t.Fatalf("malformed commit: %v", err)
	}

	// Hash-flip tamper: flip one byte inside a hash field so the object
	// stays parseable and only the object-hash comparison can catch it.
	flipped := bytes.Clone(rootObj)
	flipped[len(flipped)-1] ^= 0xff
	err := VerifyTreeBinding(f, commit, "proto/v1", [][]byte{flipped, protoObj}, v1)
	if !errors.Is(err, ErrTreeMismatch) || !strings.Contains(err.Error(), "tree object 0 hashes to") {
		t.Fatalf("hash-flip tamper: err = %v, want object-hash mismatch class", err)
	}

	// Missing segment must be reported as such, not as a downstream
	// recomputed-hash mismatch.
	err = VerifyTreeBinding(f, commit, "nope", [][]byte{rootObj}, v1)
	if !errors.Is(err, ErrTreeMismatch) || !strings.Contains(err.Error(), "not found as a directory") {
		t.Fatalf("missing segment: err = %v, want not-found class", err)
	}

	// A file segment (non-directory mode) is also not-found.
	err = VerifyTreeBinding(f, commit, "pb.yaml", [][]byte{rootObj}, v1)
	if !errors.Is(err, ErrTreeMismatch) || !strings.Contains(err.Error(), "not found as a directory") {
		t.Fatalf("file segment: err = %v, want not-found class", err)
	}

	// Final mismatch names the recomputed-tree class.
	root, _ := TreeHash(f, entries)
	err = VerifyTreeBinding(f, commit, "proto/v1", [][]byte{rootObj, protoObj}, root)
	if !errors.Is(err, ErrTreeMismatch) || !strings.Contains(err.Error(), "recomputed module tree") {
		t.Fatalf("final mismatch: err = %v, want recomputed class", err)
	}
}

// A hostile commit can reference ANY bytes by their correct hash — hash
// verification of a walk object proves reference integrity, not
// parseability. Garbage that hashes to what the commit says must be
// rejected by the parser, not walked.
func TestVerifyTreeBindingGarbageReferencedTree(t *testing.T) {
	garbage := []byte("junk")
	gh, err := ObjectHash(SHA1, "tree", garbage)
	if err != nil {
		t.Fatal(err)
	}
	commit := []byte("tree " + hex.EncodeToString(gh) + "\nauthor t <t@t> 1 +0000\n\nx\n")
	err = VerifyTreeBinding(SHA1, commit, "x", [][]byte{garbage}, gh)
	if !errors.Is(err, ErrObjectInvalid) {
		t.Fatalf("garbage-referenced tree: err = %v, want ErrObjectInvalid from the parser", err)
	}
}

// Hostile trees can carry duplicate names; the walk takes the FIRST match —
// git's own iteration order — and a legacy zero-padded 040000 directory
// mode walks like git walks it.
func TestVerifyTreeBindingHostileAndLegacyModes(t *testing.T) {
	sub := rawTree(t, [3]string{"100644", "f", strings.Repeat("11", 20)})
	subHash, err := ObjectHash(SHA1, "tree", sub)
	if err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("22", 20)

	// Duplicate "x" entries: first references sub, second references junk.
	dup := rawTree(t,
		[3]string{"40000", "x", hex.EncodeToString(subHash)},
		[3]string{"40000", "x", wrong},
	)
	dupHash, err := ObjectHash(SHA1, "tree", dup)
	if err != nil {
		t.Fatal(err)
	}
	commit := []byte("tree " + hex.EncodeToString(dupHash) + "\nauthor t <t@t> 1 +0000\n\nx\n")
	if err := VerifyTreeBinding(SHA1, commit, "x", [][]byte{dup}, subHash); err != nil {
		t.Fatalf("first-match walk: %v", err)
	}

	// Legacy zero-padded directory mode: honest git-accepted trees carry
	// 040000; the walk resolves them (parsed octal, not string equality).
	legacy := rawTree(t, [3]string{"040000", "v1", hex.EncodeToString(subHash)})
	legacyHash, err := ObjectHash(SHA1, "tree", legacy)
	if err != nil {
		t.Fatal(err)
	}
	commit = []byte("tree " + hex.EncodeToString(legacyHash) + "\nauthor t <t@t> 1 +0000\n\nx\n")
	if err := VerifyTreeBinding(SHA1, commit, "v1", [][]byte{legacy}, subHash); err != nil {
		t.Fatalf("zero-padded directory mode: %v", err)
	}

	// A blob entry (100644) with a matching name is still not a directory.
	blobby := rawTree(t, [3]string{"0100644", "v1", hex.EncodeToString(subHash)})
	blobbyHash, _ := ObjectHash(SHA1, "tree", blobby)
	commit = []byte("tree " + hex.EncodeToString(blobbyHash) + "\nauthor t <t@t> 1 +0000\n\nx\n")
	if err := VerifyTreeBinding(SHA1, commit, "v1", [][]byte{blobby}, subHash); !errors.Is(err, ErrTreeMismatch) {
		t.Fatalf("zero-padded blob mode as directory: err = %v, want not-found", err)
	}
}
