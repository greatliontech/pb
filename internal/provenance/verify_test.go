package provenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"encoding/hex"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/gittest"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/version"
	"github.com/greatliontech/stipulator/stipulate/structural"
	"pgregory.net/rapid"
)

func v(t *testing.T, s string) version.Version {
	t.Helper()
	ver, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return ver
}

// fixture is a real in-memory repository with a module at the root and
// one at a subtree, plus the raw objects provenance evidence carries.
type fixture struct {
	commitHash plumbing.Hash
	rawCommit  []byte
	rootTree   []byte // module-root tree hash, root module
	rawRoot    []byte // raw root tree object
	modTree    []byte // module-root tree hash, subtree module "mod"
}

func newFixture(t *testing.T) fixture {
	r := gittest.New(t)
	pbYaml := r.Blob("module: example.test/m\n")
	proto := r.Blob("syntax = \"proto3\";\n")
	mod := r.Tree(
		object.TreeEntry{Name: "a.proto", Mode: filemode.Regular, Hash: proto},
		object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: pbYaml},
	)
	root := r.Tree(
		object.TreeEntry{Name: "mod", Mode: filemode.Dir, Hash: mod},
		object.TreeEntry{Name: "pb.yaml", Mode: filemode.Regular, Hash: pbYaml},
	)
	commit := r.CommitTree(root, "release", time.Unix(1700000000, 0))
	return fixture{
		commitHash: commit,
		rawCommit:  r.Raw(plumbing.CommitObject, commit),
		rootTree:   root.Bytes(),
		rawRoot:    r.Raw(plumbing.TreeObject, root),
		modTree:    mod.Bytes(),
	}
}

func tagPayload(commit plumbing.Hash, name string) []byte {
	return []byte(fmt.Sprintf("object %s\ntype commit\ntag %s\n"+
		"tagger Test Signer <signer@example.com> 1700000100 +0000\n\nrelease %s\n",
		commit, name, name))
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	s := newVirtualSigner(t)
	fx := newFixture(t)
	id := s.identity()

	t.Run("happy: root module", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		vi, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root)
		if err != nil {
			t.Fatalf("Verify = %v, want nil", err)
		}
		if vi.Subject != testSubject || vi.Issuer != testIssuer {
			t.Fatalf("identity = (%q,%q), want (%q,%q)", vi.Subject, vi.Issuer, testSubject, testIssuer)
		}
		if vi.RekorIntegratedTime == 0 {
			t.Fatal("verified identity carries no transparency coordinates")
		}
	})

	t.Run("happy: subtree module walks the treePath", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "mod/v2.0.0"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit, TreePath: [][]byte{fx.rawRoot}}
		vi, err := Verify(ctx, ev, Subject{Version: v(t, "v2.0.0"), Subtree: "mod"}, fx.modTree, id, s.root)
		if err != nil {
			t.Fatalf("Verify(subtree) = %v, want nil", err)
		}
		if vi.Subject != testSubject {
			t.Fatalf("subject = %q", vi.Subject)
		}
	})

	t.Run("absent: no embedded transparency proof", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), false)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		_, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root)
		if !errors.Is(err, ErrNoTransparency) {
			t.Fatalf("Verify = %v, want ErrNoTransparency", err)
		}
	})

	t.Run("rejected: unsigned tag is not absent", func(t *testing.T) {
		ev := Evidence{Format: archive.SHA1, Tag: tagPayload(fx.commitHash, "v1.2.3"), Commit: fx.rawCommit}
		_, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root)
		if err == nil || errors.Is(err, ErrNoTransparency) {
			t.Fatalf("Verify = %v, want rejection distinct from absence", err)
		}
		if !strings.Contains(err.Error(), "evidence signature") {
			t.Fatalf("Verify = %v, want signature-stage rejection", err)
		}
	})

	t.Run("rejected: identity mismatch", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		wrong := gitprov.Identity{Issuer: testIssuer, Subject: "attacker@evil.example"}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, wrong, s.root); err == nil ||
			!strings.Contains(err.Error(), "SAN") {
			t.Fatalf("Verify = %v, want identity rejection", err)
		}
	})

	t.Run("rejected: tag names a different version", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.4"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root); err == nil ||
			!strings.Contains(err.Error(), "does not name") {
			t.Fatalf("Verify = %v, want tag-name rejection", err)
		}
	})

	t.Run("rejected: subtree tag without the subtree prefix", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v2.0.0"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit, TreePath: [][]byte{fx.rawRoot}}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v2.0.0"), Subtree: "mod"}, fx.modTree, id, s.root); err == nil ||
			!strings.Contains(err.Error(), "does not name") {
			t.Fatalf("Verify = %v, want tag-name rejection", err)
		}
	})

	t.Run("rejected: commit bytes are not the signed tag's object", func(t *testing.T) {
		// A genuine signed tag paired with different commit bytes: the
		// pairing is envelope(attacker)-supplied and must be recomputed,
		// never trusted (REQ-prov-tag-binding).
		r2 := gittest.New(t)
		other := r2.CommitTree(r2.Tree(), "other", time.Unix(1700000000, 0))
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: r2.Raw(plumbing.CommitObject, other)}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root); err == nil ||
			!strings.Contains(err.Error(), "does not match the signed tag's object") {
			t.Fatalf("Verify = %v, want commit-binding rejection", err)
		}
	})

	t.Run("rejected: tag targeting another tag, not a commit", func(t *testing.T) {
		payload := []byte(fmt.Sprintf("object %s\ntype tag\ntag v1.2.3\n"+
			"tagger Test Signer <signer@example.com> 1700000100 +0000\n\nnested\n", fx.commitHash))
		tag := s.signedTag(t, payload, true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root); err == nil ||
			!strings.Contains(err.Error(), "not a commit") {
			t.Fatalf("Verify = %v, want target-type rejection", err)
		}
	})

	t.Run("rejected: archive tree does not match the module root", func(t *testing.T) {
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.modTree, id, s.root); err == nil {
			t.Fatal("Verify = nil, want tree-binding rejection for a foreign computed tree")
		}
	})

	t.Run("rejected: format mislabel fails closed", func(t *testing.T) {
		// The stated objectFormat is attacker wire input. Relabeling
		// sha1 evidence as sha256 must fail: the signed tag's own
		// object field is 40 hex digits, not the 64 the stated format
		// requires — the tag pins its true format.
		tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
		ev := Evidence{Format: archive.SHA256, Tag: tag, Commit: fx.rawCommit}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, id, s.root); err == nil ||
			!strings.Contains(err.Error(), "is 40 hex digits, want 64") {
			t.Fatalf("Verify = %v, want format-pinned rejection naming both lengths", err)
		}
	})

	t.Run("rejected: tampered treePath", func(t *testing.T) {
		bad := append([]byte{}, fx.rawRoot...)
		bad[0] ^= 0x01
		tag := s.signedTag(t, tagPayload(fx.commitHash, "mod/v2.0.0"), true)
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit, TreePath: [][]byte{bad}}
		if _, err := Verify(ctx, ev, Subject{Version: v(t, "v2.0.0"), Subtree: "mod"}, fx.modTree, id, s.root); err == nil {
			t.Fatal("Verify = nil, want tree-walk rejection for tampered treePath")
		}
	})
}

// TestParseTagHeader pins the strict tag-header grammar directly: git
// writes exactly object/type/tag as the leading single-line headers,
// and every deviation is its own malformed-object failure.
func TestParseTagHeader(t *testing.T) {
	good := "object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntype commit\ntag v1.0.0\ntagger t <t@x> 1 +0000\n\nm\n"

	t.Run("happy", func(t *testing.T) {
		hdr, err := parseTagHeader(archive.SHA1, []byte(good))
		if err != nil {
			t.Fatalf("parseTagHeader = %v, want nil", err)
		}
		if hdr.targetType != "commit" || hdr.name != "v1.0.0" || len(hdr.object) != 20 {
			t.Fatalf("hdr = %+v", hdr)
		}
	})

	cases := map[string]struct {
		raw     string
		wantSub string
	}{
		"truncated: no newline at all": {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904", "truncated header"},
		"truncated: after object":      {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n", "truncated header"},
		"wrong first header":           {"type commit\nobject 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntag v1\n", `want "object "`},
		"object header without value":  {"object \ntype commit\ntag v1\n", `want "object "`},
		"short object hex":             {"object 4b82\ntype commit\ntag v1\n", "4 hex digits, want 40"},
		"long object hex":              {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904ff\ntype commit\ntag v1\n", "42 hex digits, want 40"},
		"non-hex object":               {"object zb825dc642cb6eb9a060e54bf8d69288fbee4904\ntype commit\ntag v1\n", "object field"},
		"missing type header":          {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntag v1\ntagger t\n", `want "type "`},
		"missing tag header":           {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntype commit\ntagger t\n", `want "tag "`},
		"headers out of order":         {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntag v1\ntype commit\n", `want "type "`},
		"empty tag name":               {"object 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ntype commit\ntag \n", `want "tag "`},
		"sha256-length hex under sha1": {"object " + strings.Repeat("ab", 32) + "\ntype commit\ntag v1\n", "64 hex digits, want 40"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTagHeader(archive.SHA1, []byte(c.raw)); err == nil ||
				!strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("parseTagHeader = %v, want error containing %q", err, c.wantSub)
			}
		})
	}

	t.Run("unknown format is rejected before any parsing", func(t *testing.T) {
		if _, err := parseTagHeader(archive.ObjectFormat("sha512"), []byte(good)); err == nil ||
			!strings.Contains(err.Error(), "unknown object format") {
			t.Fatalf("parseTagHeader(sha512) = %v, want the format stage's own error", err)
		}
	})

	t.Run("leading empty line is a header mismatch, not truncation", func(t *testing.T) {
		if _, err := parseTagHeader(archive.SHA1, []byte("\nobject x\n")); err == nil ||
			!strings.Contains(err.Error(), `want "object "`) {
			t.Fatalf("parseTagHeader(leading newline) = %v, want object-header mismatch", err)
		}
	})

	t.Run("sha256 format sizes the object field", func(t *testing.T) {
		raw := "object " + strings.Repeat("ab", 32) + "\ntype commit\ntag v1\n"
		hdr, err := parseTagHeader(archive.SHA256, []byte(raw))
		if err != nil || len(hdr.object) != 32 {
			t.Fatalf("parseTagHeader(sha256) = (%+v, %v), want 32-byte object", hdr, err)
		}
	})
}

// TestVerifyBindingFailsClosedUnderCorruption proves
// REQ-prov-tag-binding as a for-all property: starting from genuinely
// verifiable evidence, any single-byte flip, truncation, or insertion
// in any evidence component — tag bytes, commit bytes, a treePath
// element, or the computed archive tree hash — either fails
// verification or (for semantically neutral mutations such as PEM
// slack in the signature armor) still verifies the exact same subject.
// There is no corruption that verifies with a different identity, and
// no component whose corruption is silently absorbed.
func TestVerifyBindingFailsClosedUnderCorruption(t *testing.T) {
	ctx := context.Background()
	s := newVirtualSigner(t)
	fx := newFixture(t)
	id := s.identity()
	tag := s.signedTag(t, tagPayload(fx.commitHash, "mod/v2.0.0"), true)
	sub := Subject{Version: v(t, "v2.0.0"), Subtree: "mod"}

	genuine, err := Verify(ctx, Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit,
		TreePath: [][]byte{fx.rawRoot}}, sub, fx.modTree, id, s.root)
	if err != nil {
		t.Fatalf("uncorrupted evidence does not verify: %v", err)
	}

	corrupt := func(rt *rapid.T, b []byte) []byte {
		pos := rapid.IntRange(0, len(b)-1).Draw(rt, "pos")
		out := make([]byte, 0, len(b)+1)
		switch rapid.IntRange(0, 2).Draw(rt, "op") {
		case 0:
			out = append(out, b...)
			nb := rapid.Byte().Draw(rt, "byte")
			if nb == out[pos] {
				nb ^= 0xff
			}
			out[pos] = nb
		case 1:
			if pos == 0 {
				pos = 1
			}
			out = append(out, b[:pos]...)
		default:
			out = append(out, b[:pos]...)
			out = append(out, rapid.Byte().Draw(rt, "ins"))
			out = append(out, b[pos:]...)
		}
		return out
	}

	rapid.Check(t, func(rt *rapid.T) {
		ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit, TreePath: [][]byte{fx.rawRoot}}
		computed := fx.modTree
		switch rapid.IntRange(0, 3).Draw(rt, "component") {
		case 0:
			ev.Tag = corrupt(rt, ev.Tag)
		case 1:
			ev.Commit = corrupt(rt, ev.Commit)
		case 2:
			ev.TreePath = [][]byte{corrupt(rt, fx.rawRoot)}
		default:
			computed = corrupt(rt, fx.modTree)
		}
		vi, err := Verify(ctx, ev, sub, computed, id, s.root)
		if (vi == nil) == (err == nil) {
			t.Fatalf("partial success: vi=%v err=%v", vi, err)
		}
		if err == nil && *vi != *genuine {
			t.Fatalf("corruption minted a different identity:\ngot  %+v\nwant %+v", *vi, *genuine)
		}
	})
}

// TestRecord pins the evidence→lockfile record mapping
// (REQ-lock-provenance-record): the type literal, the format carried
// through, the hex hash of the TAG (the signed object — not the
// commit), and the verified identity's two fields in their places.
func TestRecord(t *testing.T) {
	s := newVirtualSigner(t)
	fx := newFixture(t)
	tag := s.signedTag(t, tagPayload(fx.commitHash, "v1.2.3"), true)
	ev := Evidence{Format: archive.SHA1, Tag: tag, Commit: fx.rawCommit}

	vi, err := Verify(context.Background(), ev, Subject{Version: v(t, "v1.2.3")}, fx.rootTree, s.identity(), s.root)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	rec, err := Record(ev, vi)
	if err != nil {
		t.Fatalf("Record = %v, want nil", err)
	}
	if rec.Type != "git-signed-tag" {
		t.Fatalf("Type = %q", rec.Type)
	}
	if rec.ObjectFormat != "sha1" {
		t.Fatalf("ObjectFormat = %q", rec.ObjectFormat)
	}
	wantTag, err := archive.ObjectHash(archive.SHA1, "tag", tag)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Object != hex.EncodeToString(wantTag) {
		t.Fatalf("Object = %q, want the signed tag's own hash %x", rec.Object, wantTag)
	}
	wantCommit, err := archive.ObjectHash(archive.SHA1, "commit", fx.rawCommit)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Object == hex.EncodeToString(wantCommit) {
		t.Fatal("Object records the commit hash; the signed object is the tag")
	}
	if rec.SAN != testSubject || rec.Issuer != testIssuer {
		t.Fatalf("identity = (%q,%q), want (%q,%q)", rec.SAN, rec.Issuer, testSubject, testIssuer)
	}
	if rec == (lockfile.Provenance{}) {
		t.Fatal("record is the zero value, which the lockfile reads as none")
	}

	t.Run("unknown format fails closed", func(t *testing.T) {
		bad := Evidence{Format: archive.ObjectFormat("sha512"), Tag: tag, Commit: fx.rawCommit}
		if _, err := Record(bad, vi); err == nil ||
			!strings.Contains(err.Error(), "hash tag") {
			t.Fatalf("Record(bad format) = %v, want hash-stage error", err)
		}
	})
}

// TestProvenanceImportsCarryNoNetworkCapability pins REQ-prov-offline
// structurally at the direct-import altitude: the provenance package
// can reach the network only through the audited offline surfaces of
// gitprov and archive — no net, no http, no service client can even be
// referenced without failing this analyzer. The behavioral half is
// gitprov's own no-network witness over the same verification stack.
func TestProvenanceImportsCarryNoNetworkCapability(t *testing.T) {
	structural.ImportAllowlist(t, "github.com/greatliontech/pb/internal/provenance", map[string]structural.ImportRule{
		"github.com/greatliontech/pb/internal/provenance": {
			Internal: []string{
				"github.com/greatliontech/pb/internal/archive",
				"github.com/greatliontech/pb/internal/lockfile",
				"github.com/greatliontech/pb/internal/version",
			},
			ThirdParty: []string{
				"github.com/greatliontech/gitprov",
			},
			RestrictStandardLibrary: true,
			StandardLibrary: []string{
				"bytes", "context", "encoding/base64", "encoding/hex",
				"encoding/json", "errors", "fmt",
			},
		},
	})
}
