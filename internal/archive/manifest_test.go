package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"
)

func content(s string) (h [32]byte, n int64) {
	return sha256.Sum256([]byte(s)), int64(len(s))
}

func file(path string, exec bool, body string) FileInfo {
	h, n := content(body)
	return FileInfo{Path: path, Exec: exec, Size: n, SHA256: h}
}

// The manifest is the exact canonical byte encoding: header, then
// "<mode> <sha256> <path>" lines LF-terminated in ascending raw-byte path
// order.
func TestManifestGolden(t *testing.T) {
	files := []FileInfo{
		file("proto/z.proto", false, "syntax = \"proto3\";"),
		file("pb.yaml", false, "module: example.com/m\n"),
		file("tools/gen.sh", true, "#!/bin/sh\n"),
	}
	got, err := Manifest(files)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	sum := func(body string) string {
		h := sha256.Sum256([]byte(body))
		return hex.EncodeToString(h[:])
	}
	want := "pb-module-manifest/v1\n" +
		"100644 " + sum("module: example.com/m\n") + " pb.yaml\n" +
		"100644 " + sum("syntax = \"proto3\";") + " proto/z.proto\n" +
		"100755 " + sum("#!/bin/sh\n") + " tools/gen.sh\n"
	if string(got) != want {
		t.Fatalf("manifest mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	// Hard-pinned digest vector: independent of the code path and the test's
	// own hash assembly. Recompute externally as
	//   sha256(header + sorted "<mode> <sha256(content)> <path>\n" lines).
	const golden = "pb1:abbab53bf00fa63aa0ce50964de5f418333bd3f7c55cac38d325b406100a0f63"
	if d := Digest(got); d != golden {
		t.Fatalf("golden digest = %s, want %s", d, golden)
	}
}

func TestDigestForm(t *testing.T) {
	m, err := Manifest([]FileInfo{file("a.proto", false, "x")})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	d := Digest(m)
	sum := sha256.Sum256(m)
	if want := "pb1:" + hex.EncodeToString(sum[:]); d != want {
		t.Fatalf("digest %q, want %q", d, want)
	}
	if len(d) != len("pb1:")+64 || d != strings.ToLower(d) {
		t.Fatalf("digest %q is not pb1: plus 64 lowercase hex digits", d)
	}
}

// Digest purity: for any generated file set and any permutation of it, the
// digest is identical — it depends only on the file set, never on input
// order.
func TestDigestOrderIndependence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 60).Draw(t, "n")
		files := make([]FileInfo, 0, n)
		for i := range n {
			files = append(files, file(
				fmt.Sprintf("dir%d/f%d.proto", rapid.IntRange(0, 8).Draw(t, "dir"), i),
				rapid.Bool().Draw(t, "exec"),
				fmt.Sprintf("body-%d-%s", i, rapid.StringN(-1, 8, 8).Draw(t, "body")),
			))
		}
		ref, err := Manifest(files)
		if err != nil {
			t.Fatalf("Manifest: %v", err)
		}
		// Fisher–Yates with rapid-drawn indices, so counterexamples shrink.
		shuffled := make([]FileInfo, len(files))
		copy(shuffled, files)
		for i := len(shuffled) - 1; i > 0; i-- {
			j := rapid.IntRange(0, i).Draw(t, "j")
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
		m, err := Manifest(shuffled)
		if err != nil {
			t.Fatalf("Manifest(shuffled): %v", err)
		}
		if Digest(m) != Digest(ref) {
			t.Fatalf("digest depends on input order")
		}
	})
}

func TestManifestEmptyFileSet(t *testing.T) {
	m, err := Manifest(nil)
	if err != nil {
		t.Fatalf("Manifest(nil): %v", err)
	}
	if string(m) != "pb-module-manifest/v1\n" {
		t.Fatalf("empty manifest = %q", m)
	}
}

// Raw-byte ordering: paths sort as bytes, so "Z" (0x5a) precedes "a" (0x61)
// and multi-byte UTF-8 sorts after ASCII.
func TestManifestByteOrder(t *testing.T) {
	m, err := Manifest([]FileInfo{
		file("a.proto", false, "1"),
		file("Z.proto", false, "2"),
		file("é.proto", false, "3"), // é: 0xc3 0xa9
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(m), "\n"), "\n")
	gotOrder := []string{lines[1], lines[2], lines[3]}
	for i, wantSuffix := range []string{" Z.proto", " a.proto", " é.proto"} {
		if !strings.HasSuffix(gotOrder[i], wantSuffix) {
			t.Fatalf("line %d = %q, want suffix %q", i, gotOrder[i], wantSuffix)
		}
	}
}

// A module file strictly below the root invalidates the file set on
// both the creation and verification paths — they share this
// validation (REQ-archive-nested-module); the root's own module file
// is the declared-module case, not nesting.
func TestNestedModuleFileRejected(t *testing.T) {
	root := FileInfo{Path: ModuleFileName, Size: 1}
	proto := FileInfo{Path: "a.proto", Size: 1}
	if _, err := Manifest([]FileInfo{root, proto}); err != nil {
		t.Fatalf("root module file rejected: %v", err)
	}
	for _, nested := range []string{"sub/" + ModuleFileName, "a/b/" + ModuleFileName} {
		if _, err := Manifest([]FileInfo{root, {Path: nested, Size: 1}}); !errors.Is(err, ErrNestedModule) {
			t.Fatalf("%s: err = %v, want ErrNestedModule", nested, err)
		}
	}
	// A file merely named like the module file with a suffix or in a
	// name that only contains it is not a module file.
	for _, ok := range []string{"sub/pb.yaml.bak", "sub/xpb.yaml"} {
		if _, err := Manifest([]FileInfo{{Path: ok, Size: 1}}); err != nil {
			t.Fatalf("%s: %v, want accepted", ok, err)
		}
	}
}

// For any valid file set, a module file injected at any directory
// strictly below the root — and only there — invalidates it
// (REQ-archive-nested-module).
func TestNestedModuleProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(t, "n")
		files := make([]FileInfo, 0, n+2)
		for i := range n {
			files = append(files, FileInfo{
				Path: fmt.Sprintf("dir%d/f%d.proto", rapid.IntRange(0, 5).Draw(t, "dir"), i),
				Size: 1,
			})
		}
		// The root's own module file is the declared-module case.
		files = append(files, FileInfo{Path: ModuleFileName, Size: 1})
		if _, err := Manifest(files); err != nil {
			t.Fatalf("valid set rejected: %v", err)
		}
		depth := rapid.IntRange(1, 3).Draw(t, "depth")
		segs := make([]string, depth)
		for i := range segs {
			segs[i] = fmt.Sprintf("d%d", rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("seg%d", i)))
		}
		nested := strings.Join(segs, "/") + "/" + ModuleFileName
		if _, err := Manifest(append(files, FileInfo{Path: nested, Size: 1})); !errors.Is(err, ErrNestedModule) {
			t.Fatalf("nested %s: err = %v, want ErrNestedModule", nested, err)
		}
	})
}

func TestPathValidation(t *testing.T) {
	bad := []struct {
		name, path string
	}{
		{"empty", ""},
		{"leading slash", "/a.proto"},
		{"trailing slash", "a/"},
		{"empty segment", "a//b.proto"},
		{"dot segment", "a/./b.proto"},
		{"dotdot segment", "../b.proto"},
		{"newline", "a\nb.proto"},
		{"nul", "a\x00b"},
		{"del", "a\x7fb"},
		{"invalid utf8", "a\xffb.proto"},
		{"reserved con", "con"},
		{"reserved con ext", "sub/CON.proto"},
		{"reserved prn", "prn.proto"},
		{"reserved aux", "AUX"},
		{"reserved nul", "sub/nul.txt"},
		{"reserved com1 ext", "com1.txt"},
		{"reserved lpt9", "LPT9"},
		{"reserved com superscript", "com¹"},
		{"reserved lpt superscript ext", "sub/LPT².proto"},
		{"reserved superscript three", "com³"},
		{"colon", "com:"},
		{"backslash", `a\b.proto`},
		{"less than", "a<b.proto"},
		{"greater than", "a>b.proto"},
		{"double quote", `a"b.proto`},
		{"pipe", "a|b.proto"},
		{"question mark", "a?.proto"},
		{"asterisk", "a*.proto"},
		{"trailing dot segment", "sub./x.proto"},
		{"trailing dot file", "a/x."},
		{"trailing space segment", "sub /x.proto"},
		{"trailing space file", "a/x "},
	}
	for _, tc := range bad {
		if _, err := Manifest([]FileInfo{{Path: tc.path}}); !errors.Is(err, ErrPathInvalid) {
			t.Errorf("%s (%q): err = %v, want ErrPathInvalid", tc.name, tc.path, err)
		}
	}
	good := []string{
		"a.proto", "a/b/c.proto", "com10.proto", "console.proto",
		"with space.proto", "é/世界.proto", "lpt0", "COM.proto",
		"coma", "lptx.proto", "aux0", ".proto", ".con", "com¹0",
	}
	for _, p := range good {
		if _, err := Manifest([]FileInfo{{Path: p}}); err != nil {
			t.Errorf("%q: unexpected err %v", p, err)
		}
	}
}

func TestPathCollisions(t *testing.T) {
	cases := []struct {
		name  string
		files []FileInfo
		msg   string // the collision class named in the error
	}{
		{"duplicate", []FileInfo{{Path: "a.proto"}, {Path: "a.proto"}}, "duplicate path"},
		{"ascii case fold", []FileInfo{{Path: "Foo/Bar.proto"}, {Path: "foo/bAR.proto"}}, "equal under case folding"},
		{"kelvin sign folds to k", []FileInfo{{Path: "K.proto"}, {Path: "k.proto"}}, "equal under case folding"},
		{"file and directory", []FileInfo{{Path: "a"}, {Path: "a/b.proto"}}, "is both a file and a directory"},
		{"file and deep directory", []FileInfo{{Path: "x/y"}, {Path: "x/y/z/w.proto"}}, "is both a file and a directory"},
		{"case-folded file vs directory", []FileInfo{{Path: "A"}, {Path: "a/b.proto"}}, "equal under case folding"},
		{"case-folded file vs deep directory", []FileInfo{{Path: "x/Y"}, {Path: "x/y/z/w.proto"}}, "equal under case folding"},
		// Mixed byte/fold case: "a" is byte-identically a directory (via
		// a/b.proto) and fold-equal to directory "A"; the fold class is
		// reported (byte-equal is a subcase of fold-equal), and the named
		// offender is deterministic (input order).
		{"mixed byte and fold dir collision", []FileInfo{{Path: "a/b.proto"}, {Path: "A/c.proto"}, {Path: "a"}}, "equal under case folding"},
	}
	for _, tc := range cases {
		_, err := Manifest(tc.files)
		if !errors.Is(err, ErrPathCollision) {
			t.Errorf("%s: err = %v, want ErrPathCollision", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err %q does not name collision class %q", tc.name, err, tc.msg)
		}
	}
	// Distinct-but-similar paths are not collisions.
	ok := []FileInfo{{Path: "a"}, {Path: "ab/c.proto"}, {Path: "a.proto"}}
	if _, err := Manifest(ok); err != nil {
		t.Errorf("non-colliding set rejected: %v", err)
	}
	// Directories fold-equal only to each other are permitted (Go module
	// zips make the same call): extraction may merge them case-insensitively
	// but loses no file, and digest verification reads archive members, not
	// the filesystem.
	dirsDiverge := []FileInfo{{Path: "A/x.proto"}, {Path: "a/y.proto"}}
	if _, err := Manifest(dirsDiverge); err != nil {
		t.Errorf("fold-equal directories rejected: %v", err)
	}
}

func TestSizeLimit(t *testing.T) {
	at := []FileInfo{
		{Path: "a", Size: MaxTotalSize - 1},
		{Path: "b", Size: 1},
	}
	if _, err := Manifest(at); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	over := []FileInfo{
		{Path: "a", Size: MaxTotalSize},
		{Path: "b", Size: 1},
	}
	if _, err := Manifest(over); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over limit: err = %v, want ErrTooLarge", err)
	}
	if _, err := Manifest([]FileInfo{{Path: "a", Size: -1}}); !errors.Is(err, ErrPathInvalid) {
		t.Fatalf("negative size: err = %v, want ErrPathInvalid", err)
	}
	// The limit is cumulative across the whole set, not per file or per
	// adjacent pair.
	cumulative := []FileInfo{
		{Path: "a", Size: MaxTotalSize / 2},
		{Path: "b", Size: MaxTotalSize / 2},
		{Path: "c", Size: MaxTotalSize / 2},
	}
	if _, err := Manifest(cumulative); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cumulative overflow: err = %v, want ErrTooLarge", err)
	}
	// Sizes that would wrap int64 must be rejected, not wrapped past the
	// limit check.
	wrap := []FileInfo{
		{Path: "a", Size: 1},
		{Path: "b", Size: math.MaxInt64},
	}
	if _, err := Manifest(wrap); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("overflowing sizes: err = %v, want ErrTooLarge", err)
	}
}

func TestModeStrings(t *testing.T) {
	if got := (FileInfo{Exec: false}).Mode(); got != "100644" {
		t.Fatalf("plain mode = %q", got)
	}
	if got := (FileInfo{Exec: true}).Mode(); got != "100755" {
		t.Fatalf("exec mode = %q", got)
	}
}

// Property: for any generated valid path and any distinct case variant of
// it (runes replaced within their simple-fold cycles), the pair is rejected
// as a collision in either order. The generator is total â every draw is a
// valid path with at least one flip forced; no rejection sampling.
func TestCaseFoldCollisionProperty(t *testing.T) {
	letters := []rune("abcdefghijklmnopqrstuvwxyzäöüσK") // ends with KELVIN SIGN
	rapid.Check(t, func(t *rapid.T) {
		// Each segment leads with 'z': no reserved device name starts with
		// z, and the alphabet has no digits, so no segment can spell one.
		segs := rapid.IntRange(1, 3).Draw(t, "segs")
		path := make([]rune, 0, 24)
		for s := range segs {
			if s > 0 {
				path = append(path, '/')
			}
			path = append(path, 'z')
			for range rapid.IntRange(0, 3).Draw(t, "extra") {
				path = append(path, rapid.SampledFrom(letters).Draw(t, "letter"))
			}
		}
		path = append(path, []rune(".proto")...)
		orig := string(path)
		if err := validatePath(orig); err != nil {
			t.Fatalf("generator produced invalid path %q: %v", orig, err)
		}

		// Flip a drawn subset of foldable runes, then force one flip at a
		// drawn foldable position ('z' always qualifies).
		variant := []rune(orig)
		var foldable []int
		for i, r := range variant {
			if unicode.SimpleFold(r) != r {
				foldable = append(foldable, i)
				if rapid.Bool().Draw(t, "flip") {
					variant[i] = unicode.SimpleFold(r)
				}
			}
		}
		forced := rapid.SampledFrom(foldable).Draw(t, "forced")
		variant[forced] = unicode.SimpleFold([]rune(orig)[forced])

		a := FileInfo{Path: orig}
		b := FileInfo{Path: string(variant)}
		if _, err := Manifest([]FileInfo{a, b}); !errors.Is(err, ErrPathCollision) {
			t.Fatalf("%q vs %q accepted, want collision", orig, string(variant))
		}
		if _, err := Manifest([]FileInfo{b, a}); !errors.Is(err, ErrPathCollision) {
			t.Fatalf("collision detection is order-dependent for %q / %q", orig, string(variant))
		}
	})
}
