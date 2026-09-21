package archive

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/testing/scratchtest"

	"pgregory.net/rapid"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func writeZip(t *testing.T, files []File) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	digest, err := WriteZip(&buf, files)
	if err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	return buf.Bytes(), digest
}

func bodies(files ...struct {
	path, body string
	exec       bool
}) []File {
	var out []File
	for _, f := range files {
		out = append(out, File{Path: f.path, Exec: f.exec, Body: strings.NewReader(f.body)})
	}
	return out
}

func sampleFiles() []File {
	return bodies(
		struct {
			path, body string
			exec       bool
		}{"pb.yaml", "module: example.com/m\n", false},
		struct {
			path, body string
			exec       bool
		}{"proto/z.proto", "syntax = \"proto3\";", false},
		struct {
			path, body string
			exec       bool
		}{"tools/gen.sh", "#!/bin/sh\n", true},
	)
}

// Round trip: WriteZip's digest equals the manifest digest of the same file
// set, VerifyZip accepts the bytes against it, and the verified file set
// carries the produced paths, modes, sizes, and hashes.
func TestZipRoundTrip(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())
	// The produced digest matches the manifest core's golden vector for the
	// identical file set (same fixture as TestManifestGolden).
	const golden = "pb1:abbab53bf00fa63aa0ce50964de5f418333bd3f7c55cac38d325b406100a0f63"
	if digest != golden {
		t.Fatalf("WriteZip digest = %s, want %s", digest, golden)
	}
	infos, err := VerifyZip(bytes.NewReader(data), int64(len(data)), digest)
	if err != nil {
		t.Fatalf("VerifyZip: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("verified %d files, want 3", len(infos))
	}
	if infos[2].Path != "tools/gen.sh" || !infos[2].Exec {
		t.Fatalf("exec mode lost: %+v", infos[2])
	}
	if infos[0].Path != "pb.yaml" || infos[0].Size != int64(len("module: example.com/m\n")) {
		t.Fatalf("unexpected first entry: %+v", infos[0])
	}
}

// Property: any generated file set round-trips — produce, verify, and the
// verified set reproduces paths, exec bits, sizes, and content hashes.
func TestZipRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(t, "n")
		var files []File
		var want []FileInfo
		for i := range n {
			body := rapid.StringN(0, 64, -1).Draw(t, "body")
			exec := rapid.Bool().Draw(t, "exec")
			path := fmt.Sprintf("d%d/f%d.proto", rapid.IntRange(0, 4).Draw(t, "dir"), i)
			files = append(files, File{Path: path, Exec: exec, Body: strings.NewReader(body)})
			want = append(want, file(path, exec, body))
		}
		var buf bytes.Buffer
		digest, err := WriteZip(&buf, files)
		if err != nil {
			t.Fatalf("WriteZip: %v", err)
		}
		wantManifest, err := Manifest(want)
		if err != nil {
			t.Fatalf("Manifest: %v", err)
		}
		if Digest(wantManifest) != digest {
			t.Fatalf("WriteZip digest disagrees with manifest digest")
		}
		infos, err := VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), digest)
		if err != nil {
			t.Fatalf("VerifyZip: %v", err)
		}
		gotManifest, err := Manifest(infos)
		if err != nil {
			t.Fatalf("Manifest(verified): %v", err)
		}
		if !bytes.Equal(gotManifest, wantManifest) {
			t.Fatalf("verified file set differs:\n%s\nvs\n%s", gotManifest, wantManifest)
		}
	})
}

// A corrupted member must fail with a digest mismatch, never be accepted.
func TestZipCorruptedMember(t *testing.T) {
	files := sampleFiles()
	data, digest := writeZip(t, files)

	// Rebuild the zip with one member's content changed; the container is
	// valid, only the bytes differ.
	var tampered bytes.Buffer
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("reading produced zip: %v", err)
	}
	zw := zip.NewWriter(&tampered)
	for _, m := range zr.File {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: m.Name, Method: zip.Deflate, Modified: zipEpoch})
		if err != nil {
			t.Fatal(err)
		}
		if m.Name == "proto/z.proto" {
			w.Write([]byte("syntax = \"proto2\";")) // tampered
			continue
		}
		rc, _ := m.Open()
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
	}
	zw.Close()

	if _, err := VerifyZip(bytes.NewReader(tampered.Bytes()), int64(tampered.Len()), digest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("tampered member: err = %v, want ErrDigestMismatch", err)
	}
}

// Extra and missing members change the recomputed manifest: digest mismatch.
// The verification side of REQ-archive-nested-module: a crafted zip
// smuggling a nested module file fails VerifyZip regardless of digest.
func TestZipNestedModuleRejected(t *testing.T) {
	var crafted bytes.Buffer
	zw := zip.NewWriter(&crafted)
	for _, m := range []struct{ name, content string }{
		{"a.proto", "syntax = \"proto3\";"},
		{"sub/" + module.ModuleFileName, "module: example.com/n\n"},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: m.name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyZip(bytes.NewReader(crafted.Bytes()), int64(crafted.Len()), DigestPrefix+strings.Repeat("0", 64))
	if !errors.Is(err, ErrNestedModule) {
		t.Fatalf("err = %v, want ErrNestedModule", err)
	}
}

func TestZipMemberSetMismatch(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())

	var extra bytes.Buffer
	zw := zip.NewWriter(&extra)
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, m := range zr.File {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: m.Name, Method: zip.Deflate})
		rc, _ := m.Open()
		io.Copy(w, rc)
		rc.Close()
	}
	w, _ := zw.Create("sneaked.proto")
	w.Write([]byte("syntax = \"proto3\";"))
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(extra.Bytes()), int64(extra.Len()), digest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("extra member: err = %v, want ErrDigestMismatch", err)
	}

	var missing bytes.Buffer
	zw = zip.NewWriter(&missing)
	for _, m := range zr.File {
		if m.Name == "pb.yaml" {
			continue
		}
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: m.Name, Method: zip.Deflate})
		rc, _ := m.Open()
		io.Copy(w, rc)
		rc.Close()
	}
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(missing.Bytes()), int64(missing.Len()), digest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("missing member: err = %v, want ErrDigestMismatch", err)
	}
}

// Flipping a member's exec bit changes its manifest mode: digest mismatch
// (mode is derived from recorded attributes, REQ-archive-zip-mode).
func TestZipModeTamper(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())
	var tampered bytes.Buffer
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	zw := zip.NewWriter(&tampered)
	for _, m := range zr.File {
		hdr := &zip.FileHeader{Name: m.Name, Method: zip.Deflate, Modified: zipEpoch}
		if m.Name == "pb.yaml" {
			hdr.SetMode(0o755) // was 0644
		} else {
			hdr.SetMode(m.Mode())
		}
		w, _ := zw.CreateHeader(hdr)
		rc, _ := m.Open()
		io.Copy(w, rc)
		rc.Close()
	}
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(tampered.Bytes()), int64(tampered.Len()), digest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mode tamper: err = %v, want ErrDigestMismatch", err)
	}
}

// Container-level rejections: duplicate members, unsupported compression.
// Directory entries are ignored.
func TestZipContainerRejections(t *testing.T) {
	digestOf := func(infos ...FileInfo) string {
		m, err := Manifest(infos)
		if err != nil {
			t.Fatal(err)
		}
		return Digest(m)
	}

	var dup bytes.Buffer
	zw := zip.NewWriter(&dup)
	for range 2 {
		w, _ := zw.Create("a.proto")
		w.Write([]byte("x"))
	}
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(dup.Bytes()), int64(dup.Len()), digestOf(file("a.proto", false, "x"))); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("duplicate member: err = %v, want ErrZipInvalid", err)
	}

	var bad bytes.Buffer
	zw = zip.NewWriter(&bad)
	// Register a pass-through compressor so a method-12 (bzip2) member is
	// craftable; the verifier must reject it before ever decompressing.
	zw.RegisterCompressor(12, func(w io.Writer) (io.WriteCloser, error) {
		return nopWriteCloser{w}, nil
	})
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.proto", Method: 12})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x"))
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(bad.Bytes()), int64(bad.Len()), digestOf(file("a.proto", false, "x"))); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("bzip2 member: err = %v, want ErrZipInvalid", err)
	}

	// A declared-size lie cannot be crafted through archive/zip's writer
	// (it always records true sizes); malformed-container exploration is
	// FuzzVerifyZip's job.

	// Directory entries are ignored.
	var withDir bytes.Buffer
	zw = zip.NewWriter(&withDir)
	if _, err := zw.Create("proto/"); err != nil {
		t.Fatal(err)
	}
	w, _ = zw.Create("proto/a.proto")
	w.Write([]byte("x"))
	zw.Close()
	if _, err := VerifyZip(bytes.NewReader(withDir.Bytes()), int64(withDir.Len()), digestOf(file("proto/a.proto", false, "x"))); err != nil {
		t.Fatalf("directory entry not ignored: %v", err)
	}
}

// Zip member names that violate path rules (absolute, dot-dot, invalid
// characters) are rejected by the manifest core during verification.
func TestZipHostilePaths(t *testing.T) {
	for _, name := range []string{"../escape.proto", "/abs.proto", "a\\b.proto", "con"} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		w.Write([]byte("x"))
		zw.Close()
		_, err := VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "pb1:0000000000000000000000000000000000000000000000000000000000000000")
		if !errors.Is(err, ErrPathInvalid) && !errors.Is(err, ErrZipInvalid) {
			t.Errorf("hostile member name %q: err = %v, want ErrPathInvalid/ErrZipInvalid", name, err)
		}
	}
}

// Extraction materializes content with no execute bit and no world-writable
// mode, regardless of the manifest mode (REQ-archive-no-exec-materialization).
func TestExtractNoExec(t *testing.T) {
	dir := scratchtest.Dir(t)
	data, digest := writeZip(t, sampleFiles())
	if err := ExtractZip(dir, bytes.NewReader(data), int64(len(data)), digest); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	var seen int
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		seen++
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if st.Mode()&0o111 != 0 {
			t.Errorf("%s extracted executable (mode %v)", path, st.Mode())
		}
		if st.Mode()&0o002 != 0 {
			t.Errorf("%s extracted world-writable (mode %v)", path, st.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("extracted %d files, want 3", seen)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tools", "gen.sh"))
	if err != nil || string(got) != "#!/bin/sh\n" {
		t.Fatalf("extracted content mismatch: %q, %v", got, err)
	}
}

// Extraction refuses a zip that fails verification: nothing is written.
func TestExtractRefusesBadDigest(t *testing.T) {
	dir := scratchtest.Dir(t)
	data, _ := writeZip(t, sampleFiles())
	err := ExtractZip(dir, bytes.NewReader(data), int64(len(data)), "pb1:0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v, want ErrDigestMismatch", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("extraction wrote %d entries despite failed verification", len(entries))
	}
}

// FuzzVerifyZip drives the byte-parsing surface with arbitrary containers:
// it must never panic, and it must never accept bytes against an
// unsatisfiable digest.
func FuzzVerifyZip(f *testing.F) {
	data, digest := func() ([]byte, string) {
		var buf bytes.Buffer
		d, err := WriteZip(&buf, bodies(struct {
			path, body string
			exec       bool
		}{"a/b.proto", "syntax = \"proto3\";", false}))
		if err != nil {
			panic(err)
		}
		return buf.Bytes(), d
	}()
	f.Add(data)
	f.Add([]byte("PK\x03\x04garbage"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		// The all-zeros digest has no preimage among manifests (they start
		// with the header line), so acceptance is always a bug.
		const unsat = "pb1:0000000000000000000000000000000000000000000000000000000000000000"
		if _, err := VerifyZip(bytes.NewReader(b), int64(len(b)), unsat); err == nil {
			t.Fatalf("accepted arbitrary bytes against unsatisfiable digest")
		}
		// Against the real digest, only byte-identical content may verify —
		// spot-check that acceptance implies manifest equality.
		if infos, err := VerifyZip(bytes.NewReader(b), int64(len(b)), digest); err == nil {
			m, merr := Manifest(infos)
			if merr != nil || Digest(m) != digest {
				t.Fatalf("accepted zip whose verified set does not reproduce the digest")
			}
		}
	})
}

// Property: extraction of any generated file set materializes every file
// non-executable and non-world-writable, whatever the manifest modes say.
func TestExtractNoExecProperty(t *testing.T) {
	root := scratchtest.Dir(t)
	seq := 0
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "n")
		var files []File
		for i := range n {
			files = append(files, File{
				Path: fmt.Sprintf("d%d/f%d.proto", rapid.IntRange(0, 2).Draw(t, "dir"), i),
				Exec: rapid.Bool().Draw(t, "exec"),
				Body: strings.NewReader(rapid.StringN(0, 32, -1).Draw(t, "body")),
			})
		}
		var buf bytes.Buffer
		digest, err := WriteZip(&buf, files)
		if err != nil {
			t.Fatalf("WriteZip: %v", err)
		}
		seq++
		dir := filepath.Join(root, fmt.Sprintf("case%d", seq))
		if err := ExtractZip(dir, bytes.NewReader(buf.Bytes()), int64(buf.Len()), digest); err != nil {
			t.Fatalf("ExtractZip: %v", err)
		}
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			st, err := os.Stat(path)
			if err != nil {
				return err
			}
			if st.Mode()&0o111 != 0 || st.Mode()&0o002 != 0 {
				t.Fatalf("%s extracted with mode %v", path, st.Mode())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// WriteZip output bytes are independent of input order (members are written
// sorted with fixed metadata) — reproducibility is a property of the
// producer, though never of the contract.
func TestWriteZipReproducible(t *testing.T) {
	forward := sampleFiles()
	reversed := sampleFiles()
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	var a, b bytes.Buffer
	da, err := WriteZip(&a, forward)
	if err != nil {
		t.Fatal(err)
	}
	db, err := WriteZip(&b, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if da != db || !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("WriteZip output depends on input order")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

type failingWriter struct{ n, limit int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > w.limit {
		return 0, errors.New("write boom")
	}
	return len(p), nil
}

// Producer error paths: a failing content reader, a failing destination
// writer, and an invalid file set all surface errors.
func TestWriteZipErrors(t *testing.T) {
	var buf bytes.Buffer
	d, err := WriteZip(&buf, []File{{Path: "a.proto", Body: failingReader{}}})
	if err == nil || !strings.Contains(err.Error(), "a.proto") || d != "" {
		t.Fatalf("failing reader: digest %q, err = %v", d, err)
	}
	if d, err := WriteZip(&failingWriter{limit: 8}, sampleFiles()); err == nil || d != "" {
		t.Fatalf("failing writer: digest %q, err = %v", d, err)
	}
	// Path validation fails fast: nothing is streamed to w, even when the
	// invalid path is not first.
	var pre bytes.Buffer
	d, err = WriteZip(&pre, []File{
		{Path: "a.proto", Body: strings.NewReader("x")},
		{Path: "../escape", Body: strings.NewReader("x")},
	})
	if !errors.Is(err, ErrPathInvalid) || d != "" {
		t.Fatalf("invalid path: digest %q, err = %v", d, err)
	}
	// Duplicate paths pass per-path validation and are rejected by the
	// manifest core after members were streamed; the digest is still empty.
	var dup bytes.Buffer
	d, err = WriteZip(&dup, []File{
		{Path: "a.proto", Body: strings.NewReader("x")},
		{Path: "a.proto", Body: strings.NewReader("y")},
	})
	if !errors.Is(err, ErrPathCollision) || d != "" {
		t.Fatalf("duplicate path: digest %q, err = %v", d, err)
	}
}

// setEncryptedFlag sets general-purpose bit 0 on every local and central
// header in a zip.
func setEncryptedFlag(data []byte) []byte {
	out := bytes.Clone(data)
	for i := 0; i+8 < len(out); i++ {
		if bytes.HasPrefix(out[i:], []byte("PK\x03\x04")) {
			out[i+6] |= 1
		}
		if bytes.HasPrefix(out[i:], []byte("PK\x01\x02")) {
			out[i+8] |= 1
		}
	}
	return out
}

func TestZipEncryptedRejected(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())
	enc := setEncryptedFlag(data)
	_, err := VerifyZip(bytes.NewReader(enc), int64(len(enc)), digest)
	if !errors.Is(err, ErrZipInvalid) || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("encrypted member: err = %v, want ErrZipInvalid naming encryption", err)
	}
}

// A store-method member is accepted (REQ-archive-zip permits store and
// deflate) and round-trips to the same digest as its deflate form.
func TestZipStoreRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "a/b.proto", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("syntax = \"proto3\";"))
	zw.Close()
	want, err := Manifest([]FileInfo{file("a/b.proto", false, "syntax = \"proto3\";")})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Digest(want))
	if err != nil {
		t.Fatalf("store member rejected: %v", err)
	}
	if len(infos) != 1 || infos[0].Path != "a/b.proto" {
		t.Fatalf("unexpected verified set: %+v", infos)
	}
}

// Unsupported methods are rejected by the method check itself — the error
// names the method — not merely by the absence of a registered decompressor.
func TestZipMethodRejectionNamesMethod(t *testing.T) {
	var bad bytes.Buffer
	zw := zip.NewWriter(&bad)
	zw.RegisterCompressor(12, func(w io.Writer) (io.WriteCloser, error) {
		return nopWriteCloser{w}, nil
	})
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.proto", Method: 12})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x"))
	zw.Close()
	m, _ := Manifest([]FileInfo{file("a.proto", false, "x")})
	_, verr := VerifyZip(bytes.NewReader(bad.Bytes()), int64(bad.Len()), Digest(m))
	if !errors.Is(verr, ErrZipInvalid) || !strings.Contains(verr.Error(), "unsupported compression method") {
		t.Fatalf("method 12: err = %v, want unsupported-method rejection", verr)
	}
}

// Corrupted member bytes surface as ErrZipInvalid from reading, and a
// corrupted local header signature surfaces from opening — never a panic,
// never acceptance.
func TestZipCorruptLocalData(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())

	// Corrupt bytes inside the first member's compressed data, located by
	// parsing the local header (name length at offset 26, extra length at
	// 28, data at 30+both), breaking the deflate stream/CRC.
	corrupt := bytes.Clone(data)
	nameLen := int(binary.LittleEndian.Uint16(corrupt[26:28]))
	extraLen := int(binary.LittleEndian.Uint16(corrupt[28:30]))
	dataStart := 30 + nameLen + extraLen
	for j := dataStart; j < dataStart+4; j++ {
		corrupt[j] ^= 0xff
	}
	if _, err := VerifyZip(bytes.NewReader(corrupt), int64(len(corrupt)), digest); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("corrupted member data: err = %v, want ErrZipInvalid from reading", err)
	}

	// Corrupt one local header signature; the central directory stays
	// intact, so opening that member fails.
	badsig := bytes.Clone(data)
	second := bytes.Index(badsig[4:], []byte("PK\x03\x04")) + 4
	badsig[second+3] = 0xff
	if _, err := VerifyZip(bytes.NewReader(badsig), int64(len(badsig)), digest); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("corrupt local header: err = %v, want ErrZipInvalid", err)
	}
}

// A foreign zip with members in arbitrary order verifies, and the returned
// file set is in manifest (path) order regardless.
func TestVerifyZipForeignOrder(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"z.proto", "a.proto", "m/x.proto"} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		w.Write([]byte(name))
	}
	zw.Close()
	m, err := Manifest([]FileInfo{
		file("z.proto", false, "z.proto"),
		file("a.proto", false, "a.proto"),
		file("m/x.proto", false, "m/x.proto"),
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Digest(m))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a.proto", "m/x.proto", "z.proto"} {
		if infos[i].Path != want {
			t.Fatalf("infos[%d] = %q, want %q (manifest order)", i, infos[i].Path, want)
		}
	}
}

// A member whose attributes declare a non-regular type (symlink) is rejected
// at verification: it could otherwise verify as a regular file carrying the
// link target as content (REQ-archive-forbidden-entries).
func TestZipSymlinkMemberRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(0o777 | fs.ModeSymlink)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("target"))
	zw.Close()
	m, _ := Manifest([]FileInfo{file("link", true, "target")})
	_, verr := VerifyZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Digest(m))
	if !errors.Is(verr, ErrZipInvalid) || !strings.Contains(verr.Error(), "not a regular file") {
		t.Fatalf("symlink member: err = %v, want non-regular rejection", verr)
	}
}

// writeMember refuses bytes that differ from the verified FileInfo: the
// extraction pass re-reads the container, and that second read carries no
// authority of its own.
func TestWriteMemberVerifiesBytes(t *testing.T) {
	data, _ := writeZip(t, sampleFiles())
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var m *zip.File
	for _, f := range zr.File {
		if f.Name == "pb.yaml" {
			m = f
		}
	}
	dir := scratchtest.Dir(t)
	good := file("pb.yaml", false, "module: example.com/m\n")

	if err := writeMember(filepath.Join(dir, "ok"), m, good); err != nil {
		t.Fatalf("matching info: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "ok"))
	if string(got) != "module: example.com/m\n" {
		t.Fatalf("extracted content %q", got)
	}

	wrongHash := good
	wrongHash.SHA256[0] ^= 0xff
	if err := writeMember(filepath.Join(dir, "h"), m, wrongHash); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("wrong hash: err = %v, want ErrZipInvalid", err)
	}

	wrongSize := good
	wrongSize.Size--
	if err := writeMember(filepath.Join(dir, "s"), m, wrongSize); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("wrong size: err = %v, want ErrZipInvalid", err)
	}
}

// The golden fixture's zip form hashes to the same real-git tree hashes
// the entry-level golden test pins, in both object formats: the container
// walk (member discipline, exec derivation, blob hashing) introduces
// nothing of its own.
func TestZipTreeHashGolden(t *testing.T) {
	files := bodies(
		struct {
			path, body string
			exec       bool
		}{"pb.yaml", "module: example.com/m\n", false},
		struct {
			path, body string
			exec       bool
		}{"proto/v1/svc.proto", "syntax = \"proto3\";", false},
		struct {
			path, body string
			exec       bool
		}{"tools/gen.sh", "#!/bin/sh\n", true},
		struct {
			path, body string
			exec       bool
		}{"a.b", "x", false},
		struct {
			path, body string
			exec       bool
		}{"a/inner.txt", "inner", false},
		struct {
			path, body string
			exec       bool
		}{"a0", "zed", false},
	)
	data, _ := writeZip(t, files)
	for f, want := range goldenTrees {
		h, err := ZipTreeHash(f, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("%s: ZipTreeHash: %v", f, err)
		}
		if hex.EncodeToString(h) != want.root {
			t.Errorf("%s: ZipTreeHash = %x, want %s", f, h, want.root)
		}
	}
}

// Property: for any produced file set, the container-walk tree hash equals
// the entry-level TreeHash over independently blob-hashed contents — the
// zip walk loses no path, byte, or exec bit on the way to the tree.
func TestZipTreeHashMatchesEntryHashing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		f := rapid.SampledFrom([]ObjectFormat{SHA1, SHA256}).Draw(t, "format")
		n := rapid.IntRange(0, 12).Draw(t, "n")
		var files []File
		var entries []TreeEntry
		for i := range n {
			body := rapid.StringN(0, 64, -1).Draw(t, "body")
			exec := rapid.Bool().Draw(t, "exec")
			path := fmt.Sprintf("d%d/f%d.proto", rapid.IntRange(0, 4).Draw(t, "dir"), i)
			files = append(files, File{Path: path, Exec: exec, Body: strings.NewReader(body)})
			blob, err := BlobHash(f, int64(len(body)), strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, TreeEntry{Path: path, Exec: exec, Blob: blob})
		}
		var buf bytes.Buffer
		if _, err := WriteZip(&buf, files); err != nil {
			t.Fatalf("WriteZip: %v", err)
		}
		data := buf.Bytes()
		got, err := ZipTreeHash(f, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("ZipTreeHash: %v", err)
		}
		want, err := TreeHash(f, entries)
		if err != nil {
			t.Fatalf("TreeHash: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ZipTreeHash = %x, TreeHash = %x", got, want)
		}
	})
}

// ZipFile reads exactly the named member's bytes, reports absence without
// error, and propagates the shared member discipline.
func TestZipFile(t *testing.T) {
	data, _ := writeZip(t, sampleFiles())
	r := bytes.NewReader(data)

	b, ok, err := ZipFile(r, int64(len(data)), "pb.yaml")
	if err != nil || !ok {
		t.Fatalf("ZipFile(pb.yaml) = ok=%v, err=%v", ok, err)
	}
	if string(b) != "module: example.com/m\n" {
		t.Fatalf("ZipFile(pb.yaml) = %q", b)
	}

	if _, ok, err := ZipFile(r, int64(len(data)), "absent.proto"); err != nil || ok {
		t.Fatalf("ZipFile(absent) = ok=%v, err=%v, want absent without error", ok, err)
	}

	// A member named like a directory prefix of a real member is not that
	// member: matching is exact.
	if _, ok, err := ZipFile(r, int64(len(data)), "proto"); err != nil || ok {
		t.Fatalf("ZipFile(proto) = ok=%v, err=%v, want absent", ok, err)
	}

	dup := duplicateMemberZip(t)
	if _, _, err := ZipFile(bytes.NewReader(dup), int64(len(dup)), "pb.yaml"); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("duplicate-member zip: err = %v, want ErrZipInvalid", err)
	}
	if _, err := ZipTreeHash(SHA1, bytes.NewReader(dup), int64(len(dup))); !errors.Is(err, ErrZipInvalid) {
		t.Fatalf("duplicate-member zip tree hash: err = %v, want ErrZipInvalid", err)
	}
}

// duplicateMemberZip crafts a container carrying the same member name
// twice — representable in the wire format, rejected by the walk.
func duplicateMemberZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, body := range []string{"a", "b"} {
		w, err := zw.Create("pb.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// DigestZip computes without an expectation: its digest is WriteZip's, and
// VerifyZip is exactly that computation plus the comparison.
func TestDigestZipComputes(t *testing.T) {
	data, digest := writeZip(t, sampleFiles())
	d, infos, err := DigestZip(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("DigestZip: %v", err)
	}
	if d != digest {
		t.Fatalf("DigestZip = %s, WriteZip = %s", d, digest)
	}
	if len(infos) != 3 || infos[0].Path != "pb.yaml" {
		t.Fatalf("unexpected file set: %+v", infos)
	}
	if _, err := VerifyZip(bytes.NewReader(data), int64(len(data)), "pb1:"+strings.Repeat("0", 64)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("VerifyZip with wrong expectation: err = %v, want ErrDigestMismatch", err)
	}
}
