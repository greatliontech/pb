package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/greatliontech/stipulator/stipulate/structural"
)

var goldenProv = Provenance{
	Type: "git-signed-tag", ObjectFormat: "sha1",
	Object: strings.Repeat("ab", 20),
	SAN:    "https://github.com/acme/protos/.github/workflows/release.yaml@refs/tags/v1.2.3",
	Issuer: "https://token.actions.githubusercontent.com",
}

var goldenPinned = Provenance{
	Type: "git-pinned-key", ObjectFormat: "sha256",
	Object:  strings.Repeat("cd", 32),
	KeyKind: "ssh", KeyFingerprint: "SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM",
}

func goldenFile() *File {
	return &File{
		Modules: []ModulePin{
			{Path: "example.com/b", Version: "v0.1.0", Digest: "pb1:" + strings.Repeat("22", 32), Provenance: Provenance{}},
			{Path: "example.com/c", Version: "v2.0.0", Digest: "pb1:" + strings.Repeat("88", 32), Provenance: goldenPinned},
			{Path: "example.com/a", Version: "v1.2.3", Digest: "pb1:" + strings.Repeat("11", 32), Modfile: "sha256:" + strings.Repeat("33", 32), Provenance: goldenProv},
			{Path: "example.com/a", Version: "v1.0.0", Modfile: "sha256:" + strings.Repeat("44", 32), Provenance: Provenance{}},
		},
		Plugins: []PluginPin{
			{Ref: "tools/protoc-gen-local", Scheme: SchemeLocal, Binary: map[string]string{
				"linux/amd64":  "sha256:" + strings.Repeat("66", 32),
				"darwin/arm64": "sha256:" + strings.Repeat("77", 32),
			}},
			{Ref: "ghcr.io/acme/protoc-gen-x:v2", Scheme: SchemeOCI, Digest: "sha256:" + strings.Repeat("55", 32), Provenance: Provenance{}},
		},
	}
}

const goldenEncoded = `version: 1
modules:
  - path: example.com/a
    version: v1.0.0
    modfile: sha256:` + "4444444444444444444444444444444444444444444444444444444444444444" + `
    provenance: none
  - path: example.com/a
    version: v1.2.3
    digest: pb1:` + "1111111111111111111111111111111111111111111111111111111111111111" + `
    modfile: sha256:` + "3333333333333333333333333333333333333333333333333333333333333333" + `
    provenance:
      type: git-signed-tag
      objectFormat: sha1
      object: abababababababababababababababababababab
      identity:
        san: https://github.com/acme/protos/.github/workflows/release.yaml@refs/tags/v1.2.3
        issuer: https://token.actions.githubusercontent.com
  - path: example.com/b
    version: v0.1.0
    digest: pb1:` + "2222222222222222222222222222222222222222222222222222222222222222" + `
    provenance: none
  - path: example.com/c
    version: v2.0.0
    digest: pb1:` + "8888888888888888888888888888888888888888888888888888888888888888" + `
    provenance:
      type: git-pinned-key
      objectFormat: sha256
      object: cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd
      key:
        kind: ssh
        fingerprint: SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM
plugins:
  - ref: ghcr.io/acme/protoc-gen-x:v2
    scheme: oci
    digest: sha256:` + "5555555555555555555555555555555555555555555555555555555555555555" + `
    provenance: none
  - ref: tools/protoc-gen-local
    scheme: local
    binary:
      darwin/arm64: sha256:` + "7777777777777777777777777777777777777777777777777777777777777777" + `
      linux/amd64: sha256:` + "6666666666666666666666666666666666666666666666666666666666666666" + `
`

func TestEncodeGolden(t *testing.T) {
	out, err := Encode(goldenFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != goldenEncoded {
		t.Fatalf("encoded:\n%s\nwant:\n%s", out, goldenEncoded)
	}
}

// The spelled none in one escape-free quote layer is the none record
// (REQ-lock-acceptance), as every scalar admits one.
func TestParseQuotedNone(t *testing.T) {
	for _, spelling := range []string{`"none"`, `'none'`, "none"} {
		f, err := Parse([]byte("version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: " + spelling + "\n"))
		if err != nil {
			t.Fatalf("Parse(provenance: %s) = %v, want nil", spelling, err)
		}
		if f.Modules[0].Provenance != (Provenance{}) {
			t.Fatalf("Parse(provenance: %s) = %+v, want none", spelling, f.Modules[0].Provenance)
		}
	}
}

func TestParseGoldenRoundTrip(t *testing.T) {
	f, err := Parse([]byte(goldenEncoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Modules) != 4 || len(f.Plugins) != 2 {
		t.Fatalf("parsed %d modules, %d plugins", len(f.Modules), len(f.Plugins))
	}
	pin, ok := f.Module("example.com/a", "v1.2.3")
	if !ok || pin.Provenance != goldenProv {
		t.Fatalf("rich pin lost: %+v", pin)
	}
	if pin, ok := f.Module("example.com/c", "v2.0.0"); !ok || pin.Provenance != goldenPinned {
		t.Fatalf("pinned-key pin lost: %+v", pin)
	}
	if _, ok := f.Plugin("ghcr.io/acme/protoc-gen-x:v2", SchemeOCI); !ok {
		t.Fatalf("oci plugin pin lost")
	}
	lp, ok := f.Plugin("tools/protoc-gen-local", SchemeLocal)
	if !ok || lp.Binary["linux/amd64"] != "sha256:"+strings.Repeat("66", 32) {
		t.Fatalf("local plugin pin lost: %+v", lp)
	}
	// A pin satisfies only lookups in its own scheme.
	if _, ok := f.Plugin("tools/protoc-gen-local", SchemeOCI); ok {
		t.Fatal("local pin answered an oci lookup")
	}
	again, err := Encode(f)
	if err != nil || string(again) != goldenEncoded {
		t.Fatalf("round trip is not a fixed point (%v)", err)
	}
}

func TestParseRejections(t *testing.T) {
	mod := "  - path: example.com/a\n    version: v1.0.0\n    provenance: none\n"
	cases := []struct{ name, in, msg string }{
		{"empty", "", "missing version key"},
		{"wrong version", "version: 2\nmodules:\n" + mod, "unsupported lockfile version"},
		{"missing version", "modules:\n" + mod, "unsupported lockfile version"},
		{"unknown key", "version: 1\nmodules:\n" + mod + "extra: 1\n", "unknown field"},
		{"multi-doc", "version: 1\nmodules:\n" + mod + "---\nversion: 1\n", "exactly one YAML document"},
		{"merge key", "version: 1\nmodules:\n  - <<: {path: example.com/a}\n    version: v1.0.0\n    provenance: none\n", "merge keys"},
		{"top level sequence", "- version: 1\n", "top level must be a mapping"},
		{"bad path", "version: 1\nmodules:\n  - path: nodot\n    version: v1.0.0\n    provenance: none\n", "invalid module path"},
		{"no version field", "version: 1\nmodules:\n  - path: example.com/a\n    provenance: none\n", "has no version"},
		{"bad digest", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    digest: sha256:abc\n    provenance: none\n", "digest: \"sha256:abc\" is not pb1:"},
		{"bad modfile", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    modfile: pb1:abc\n    provenance: none\n", "not sha256:"},
		{"valid digest does not excuse bad modfile", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    digest: pb1:" + strings.Repeat("11", 32) + "\n    modfile: pb1:abc\n    provenance: none\n", "not sha256:"},
		{"missing provenance", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n", "missing provenance"},
		{"bad provenance scalar", "version: 1\nmodules:\n" + strings.Replace(mod, "none", "sometimes", 1), "neither none nor a record"},
		{"bad provenance type", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: pgp\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n", "unknown provenance type"},
		{"bad object format", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: md5\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n", "unknown object format"},
		{"short object", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha256\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n", "not 64 lowercase hex"},
		{"missing identity", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n", "san and issuer"},
		{"pinned key without a key", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n", "key needs a kind and a fingerprint"},
		{"pinned key with an empty key", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key: {}\n", "key needs a kind and a fingerprint"},
		{"pinned key with an ssh fingerprint spelled as openpgp's", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: 91EDFEA1C6643EA64EC693516EA5914F2DADE816\n", "is not spelled as ssh spells one"},
		{"pinned key with an openpgp fingerprint in lowercase", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: openpgp\n        fingerprint: 91edfea1c6643ea64ec693516ea5914f2dade816\n", "is not spelled as openpgp spells one"},
		{"pinned key with an identity's issuer alone", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM\n      identity:\n        issuer: y\n", "names a key, not an identity"},
		{"pinned key with an empty identity", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: SHA256:cuQ/ZG8mqAef7X0GZ19RH5baTiwTVg76NePyXAKPBfM\n      identity: {}\n", "names a key, not an identity"},
		{"signed tag with a key's fingerprint alone", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n      key:\n        fingerprint: f\n", "names an identity, not a key"},
		{"signed tag with a null key", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n      key:\n", "names an identity, not a key"},
		{"signed tag with an empty key", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n      key: {}\n", "names an identity, not a key"},
		{"image signature with a key", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance:\n      type: image-signature\n      identity:\n        san: x\n        issuer: y\n      key:\n        kind: ssh\n        fingerprint: f\n", "an image-signature record names an identity, not a key"},
		{"image signature with an empty object format", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance:\n      type: image-signature\n      objectFormat: \"\"\n      identity:\n        san: x\n        issuer: y\n", "an image-signature record names no signed object"},
		{"pinned key of an unknown kind", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: x509\n        fingerprint: f\n", `unknown key kind "x509"`},
		{"pinned key without a fingerprint", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n", "key needs a fingerprint"},
		{"pinned key with a fingerprint of no spelling", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: -x\n", `fingerprint "-x" is not spelled as ssh spells one`},
		{"pinned key with an openpgp fingerprint of neither length", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: openpgp\n        fingerprint: " + strings.Repeat("AB", 24) + "\n", "is not spelled as openpgp spells one"},
		{"pinned key beside an identity", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: f\n      identity:\n        san: x\n        issuer: y\n", "names a key, not an identity"},
		{"pinned key without an object", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-pinned-key\n      key:\n        kind: ssh\n        fingerprint: f\n", "unknown object format"},
		{"signed tag with a key", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n      key:\n        kind: ssh\n        fingerprint: f\n", "names an identity, not a key"},
		{"pinned key on a plugin entry", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: ssh\n        fingerprint: f\n", `provenance type "git-pinned-key" is not this entry's (image-signature)`},
		{"image signature on a module entry", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: image-signature\n      identity:\n        san: x\n        issuer: y\n", `is not this entry's (git-signed-tag or git-pinned-key)`},
		{"duplicate module pin", "version: 1\nmodules:\n" + mod + mod, "duplicate module pin"},
		{"space in version", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1 .0\n    provenance: none\n", "version \"v1 .0\" is not plain-scalar safe"},
		{"trailing colon version", "version: 1\nmodules:\n  - path: example.com/a\n    version: \"v1:\"\n    provenance: none\n", "not plain-scalar safe"},
		{"quoted san survives one strip", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: \"'q'\"\n        issuer: y\n", "san \"'q'\" is not plain-scalar safe"},
		{"leading dash issuer", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x@y\n        issuer: -evil\n", "issuer \"-evil\" is not plain-scalar safe"},
		{"non-ascii san", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: café\n        issuer: y\n", "not plain-scalar safe"},
		{"unquoted null version", "version: 1\nmodules:\n  - path: example.com/a\n    version: null\n    provenance: none\n", "has no version"},
		{"quoted null version", "version: 1\nmodules:\n  - path: example.com/a\n    version: \"null\"\n    provenance: none\n", "not plain-scalar safe"},
		{"escaped single-quoted san", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: 'a''b'\n        issuer: y\n", "contains escapes"},
		{"escaped double-quoted version", "version: 1\nmodules:\n  - path: example.com/a\n    version: \"a\\\"b\"\n    provenance: none\n", "contains escapes"},
		{"non-quote escape in double-quoted version", "version: 1\nmodules:\n  - path: example.com/a\n    version: \"a\\nb\"\n    provenance: none\n", "contains escapes"},
		{"empty san value", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san:\n        issuer: y\n", "san and issuer"},
		{"empty provenance record", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: {}\n", "provenance record is empty"},
		{"none spelled with an escape", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: \"non\\u0065\"\n", "neither none nor a record"},
		{"record of an empty identity alone", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      identity: {}\n", "provenance record names no type"},
		{"record of an empty object format alone", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      objectFormat: \"\"\n", "provenance record names no type"},
		{"record of an empty key alone", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      key: {}\n", "provenance record names no type"},
		{"record of empty parts and no type", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      object: \"\"\n      identity: {}\n", "provenance record names no type"},
		{"record of an empty type", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: \"\"\n      identity:\n        san: x\n        issuer: y\n", "provenance record names no type"},
		{"null key defeating strict decode", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      null: x\n", "mapping keys are strings"},
		{"null key at top level", "version: 1\nnull: x\nmodules:\n" + mod, "mapping keys are strings"},
		{"plugin digest in ref", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b@sha256:" + strings.Repeat("11", 32) + "\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "carries a digest"},
		{"plugin bad digest", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: pb1:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "not sha256:"},
		{"duplicate plugin pin", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("22", 32) + "\n    provenance: none\n", "duplicate plugin pin"},
		{"plugin missing scheme", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "unknown scheme"},
		{"plugin unknown scheme", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: remote\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "unknown scheme"},
		{"local pin with digest", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    binary:\n      linux/amd64: sha256:" + strings.Repeat("11", 32) + "\n", "local pins carry no digest"},
		{"local pin with empty digest", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    digest: \"\"\n    binary:\n      linux/amd64: sha256:" + strings.Repeat("11", 32) + "\n", "local pins carry no digest"},
		{"local pin with null digest", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    digest:\n    binary:\n      linux/amd64: sha256:" + strings.Repeat("11", 32) + "\n", "local pins carry no digest"},
		{"oci pin with null binary", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n    binary:\n", "oci pins carry no binary"},
		{"local pin with provenance key", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    provenance: none\n    binary:\n      linux/amd64: sha256:" + strings.Repeat("11", 32) + "\n", "carry no provenance key"},
		{"local pin without binary", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n", "has no binary hashes"},
		{"local pin bad platform", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    binary:\n      linux: sha256:" + strings.Repeat("11", 32) + "\n", "not <os>/<arch>"},
		{"local pin bad hash", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: protoc-gen-x\n    scheme: local\n    binary:\n      linux/amd64: pb1:" + strings.Repeat("11", 32) + "\n", "not sha256:"},
		{"oci pin with binary", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n    binary:\n      linux/amd64: sha256:" + strings.Repeat("11", 32) + "\n", "oci pins carry no binary"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.in))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err %q does not name %q", tc.name, err, tc.msg)
		}
	}
}

func TestAddUpdateSemantics(t *testing.T) {
	f := &File{}
	pin := ModulePin{Path: "example.com/a", Version: "v1.0.0", Digest: "pb1:" + strings.Repeat("11", 32)}
	if err := f.AddModule(pin); err != nil {
		t.Fatal(err)
	}
	// First-use: re-adding the same key is rejected even with identical
	// content — pins are added once.
	if err := f.AddModule(pin); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("re-add: err = %v, want ErrPinMismatch", err)
	}
	// Explicit update path may change anything, including provenance.
	pin.Digest = "pb1:" + strings.Repeat("22", 32)
	if err := f.UpdateModule(pin); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Module(pin.Path, pin.Version); got.Digest != pin.Digest {
		t.Fatalf("update did not apply")
	}
	if err := f.UpdateModule(ModulePin{Path: "example.com/x", Version: "v1.0.0"}); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("update of missing pin: err = %v", err)
	}
	if err := f.AddModule(ModulePin{Path: "nodot", Version: "v1.0.0"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid add: err = %v", err)
	}
}

func TestVerifyModule(t *testing.T) {
	f := goldenFile()
	d := "pb1:" + strings.Repeat("11", 32)
	mf := "sha256:" + strings.Repeat("33", 32)
	if err := f.VerifyModule("example.com/a", "v1.2.3", d, mf); err != nil {
		t.Fatal(err)
	}
	// Digest mismatch names module, version, expected, computed.
	wrong := "pb1:" + strings.Repeat("ff", 32)
	err := f.VerifyModule("example.com/a", "v1.2.3", wrong, mf)
	if !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("err = %v", err)
	}
	for _, part := range []string{"example.com/a", "v1.2.3", d, wrong} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("mismatch error %q does not name %q", err, part)
		}
	}
	if err := f.VerifyModule("example.com/a", "v1.2.3", d, "sha256:"+strings.Repeat("ee", 32)); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("modfile mismatch: err = %v", err)
	}
	if err := f.VerifyModule("example.com/none", "v1.0.0", d, ""); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("missing pin: err = %v", err)
	}
	// A pin without a digest (module file only) verifies any digest — there
	// is nothing pinned to enforce yet.
	if err := f.VerifyModule("example.com/a", "v1.0.0", wrong, "sha256:"+strings.Repeat("44", 32)); err != nil {
		t.Fatalf("modfile-only pin: %v", err)
	}
}

// An image-signature record on a plugin pin encodes as its type and
// identity alone and parses back to the same record
// (REQ-lock-provenance-record, REQ-lock-plugin-entry).
func TestImageSignatureRecordRoundTrips(t *testing.T) {
	rec := Provenance{Type: ProvenanceImageSignature, SAN: "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v1", Issuer: "https://token.actions.githubusercontent.com"}
	f := &File{Plugins: []PluginPin{{Ref: "ghcr.io/acme/plugin:v1", Scheme: SchemeOCI, Digest: "sha256:" + strings.Repeat("55", 32), Provenance: rec}}}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := "version: 1\nmodules:\nplugins:\n  - ref: ghcr.io/acme/plugin:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("55", 32) + "\n    provenance:\n      type: image-signature\n      identity:\n        san: " + rec.SAN + "\n        issuer: " + rec.Issuer + "\n"
	if string(out) != want {
		t.Fatalf("encoded:\n%s\nwant:\n%s", out, want)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if pin, ok := back.Plugin("ghcr.io/acme/plugin:v1", SchemeOCI); !ok || pin.Provenance != rec {
		t.Fatalf("parsed back %+v %v", pin, ok)
	}
	// A git field written under the type is refused on read as well.
	bad := strings.Replace(string(out), "      identity:", "      object: "+strings.Repeat("ab", 20)+"\n      identity:", 1)
	if _, err := Parse([]byte(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an image record carrying an object parsed: %v", err)
	}
}

// An explicit plugin update replaces the oci pin whole, any record to
// any record, and refuses a reference with no oci pin or a local pin
// (REQ-dep-update's explicit update under REQ-lock-no-silent-downgrade).
func TestUpdatePlugin(t *testing.T) {
	signed := Provenance{Type: ProvenanceImageSignature, SAN: "https://ci.example/wf", Issuer: "https://issuer.example"}
	f := &File{Plugins: []PluginPin{{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + strings.Repeat("11", 32), Provenance: signed}}}
	moved := PluginPin{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + strings.Repeat("22", 32)}
	if err := f.UpdatePlugin(moved); err != nil {
		t.Fatal(err)
	}
	if pin, ok := f.Plugin("ghcr.io/a/b:v1", SchemeOCI); !ok || !reflect.DeepEqual(pin, moved) || len(f.Plugins) != 1 {
		t.Fatalf("pins = %+v", f.Plugins)
	}
	if err := f.UpdatePlugin(PluginPin{Ref: "ghcr.io/a/c:v1", Scheme: SchemeOCI, Digest: moved.Digest}); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("an unpinned reference: %v", err)
	}
	if err := f.UpdatePlugin(PluginPin{Ref: "protoc-gen-x", Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": "sha256:" + strings.Repeat("33", 32)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a local pin: %v", err)
	}
	if err := f.UpdatePlugin(PluginPin{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an invalid pin: %v", err)
	}
}

func TestProvenanceTransition(t *testing.T) {
	none := Provenance{}
	if err := CheckProvenanceTransition(none, goldenProv); err != nil {
		t.Fatalf("upgrade from none: %v", err)
	}
	if err := CheckProvenanceTransition(goldenProv, goldenProv); err != nil {
		t.Fatalf("same record: %v", err)
	}
	if err := CheckProvenanceTransition(goldenProv, none); !errors.Is(err, ErrProvenanceDowngrade) {
		t.Fatalf("downgrade to none: %v", err)
	}
	other := goldenProv
	other.SAN = "https://github.com/evil/repo"
	if err := CheckProvenanceTransition(goldenProv, other); !errors.Is(err, ErrProvenanceDowngrade) {
		t.Fatalf("identity change: %v", err)
	}
	// A different signed object for the same (path, version) means the
	// origin tag moved — a rewrite requiring the explicit update path,
	// even under the same identity (REQ-lock-no-silent-downgrade).
	newer := goldenProv
	newer.Object = strings.Repeat("cd", 20)
	if err := CheckProvenanceTransition(goldenProv, newer); !errors.Is(err, ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "signed object") {
		t.Fatalf("same-identity object swap must be explicit: %v", err)
	}
	// A format-only change is still a different signed object.
	refmt := goldenProv
	refmt.ObjectFormat = "sha256"
	if err := CheckProvenanceTransition(goldenProv, refmt); !errors.Is(err, ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "signed object") {
		t.Fatalf("format-only change must be explicit: %v", err)
	}
	// A pinned key is the record's identity (REQ-lock-pinned-key-record):
	// another key, or an identity in its place, is another signer.
	if err := CheckProvenanceTransition(goldenPinned, goldenPinned); err != nil {
		t.Fatalf("same pinned-key record: %v", err)
	}
	otherKey := goldenPinned
	otherKey.KeyFingerprint = "SHA256:qJf90eocttnD82cEmd3HjozFS3VFv2VfFEZPKsGOxRc"
	if err := CheckProvenanceTransition(goldenPinned, otherKey); !errors.Is(err, ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "pinned key") {
		t.Fatalf("key change: %v", err)
	}
	otherKind := goldenPinned
	otherKind.KeyKind = "openpgp"
	if err := CheckProvenanceTransition(goldenPinned, otherKind); !errors.Is(err, ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "pinned key") {
		t.Fatalf("kind change: %v", err)
	}
	toIdentity := goldenProv
	toIdentity.ObjectFormat, toIdentity.Object = goldenPinned.ObjectFormat, goldenPinned.Object
	if err := CheckProvenanceTransition(goldenPinned, toIdentity); !errors.Is(err, ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "evidence type") {
		t.Fatalf("pinned key to identity: %v", err)
	}
}

// Fixed-point property: for any generated pin set, Encode is order-
// independent, parseable, and Parse . Encode is the identity on content
// with Encode idempotent.
func TestFixedPointProperty(t *testing.T) {
	hexes := "0123456789abcdef"
	// The full accepted scalar domain: printable non-space ASCII, leading
	// alphanumeric, no trailing ':' — anything narrower is a silent
	// coverage cap on the fixed-point claim.
	alnum := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var printable, printableNoAt []rune
	for c := rune('!'); c <= '~'; c++ {
		printable = append(printable, c)
		if c != '@' {
			printableNoAt = append(printableNoAt, c)
		}
	}
	rapid.Check(t, func(t *rapid.T) {
		hex64 := func(label string) string {
			return string(rapid.SliceOfN(rapid.SampledFrom([]rune(hexes)), 64, 64).Draw(t, label))
		}
		plain := func(label string, charset []rune) string {
			s := string(rapid.SampledFrom([]rune(alnum)).Draw(t, label+"First")) +
				string(rapid.SliceOfN(rapid.SampledFrom(charset), 0, 8).Draw(t, label))
			if strings.HasSuffix(s, ":") {
				s += "x"
			}
			return s
		}
		var f File
		n := rapid.IntRange(0, 5).Draw(t, "n")
		for i := range n {
			pin := ModulePin{
				Path:    fmt.Sprintf("example.com/m%d", i/2), // collide paths, differ versions
				Version: fmt.Sprintf("v%d-", i) + plain("ver", printable),
			}
			if rapid.Bool().Draw(t, "hasDigest") {
				pin.Digest = "pb1:" + hex64("digest")
			}
			if rapid.Bool().Draw(t, "hasModfile") {
				pin.Modfile = "sha256:" + hex64("modfile")
			}
			if rapid.Bool().Draw(t, "hasProv") {
				if rapid.Bool().Draw(t, "pinnedKey") {
					// The fingerprint in its kind's spelling, the whole
					// of each grammar drawn.
					kind := rapid.SampledFrom([]string{"openpgp", "ssh"}).Draw(t, "kind")
					digits := rapid.SampledFrom([]int{40, 64}).Draw(t, "pgpfplen")
					fingerprint := strings.ToUpper(string(rapid.SliceOfN(rapid.SampledFrom([]rune(hexes)), digits, digits).Draw(t, "pgpfp")))
					if kind == "ssh" {
						fingerprint = "SHA256:" + string(rapid.SliceOfN(rapid.SampledFrom([]rune(alnum+"+/")), 43, 43).Draw(t, "sshfp"))
					}
					pin.Provenance = Provenance{
						Type: "git-pinned-key", ObjectFormat: "sha256", Object: hex64("obj"),
						KeyKind: kind, KeyFingerprint: fingerprint,
					}
				} else {
					pin.Provenance = Provenance{
						Type: "git-signed-tag", ObjectFormat: "sha256", Object: hex64("obj"),
						SAN:    plain("san", printable),
						Issuer: plain("issuer", printable),
					}
				}
			}
			f.Modules = append(f.Modules, pin)
		}
		if rapid.Bool().Draw(t, "hasPlugin") {
			if rapid.Bool().Draw(t, "pluginLocal") {
				platforms := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{
					"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64",
				}), 1, 4, rapid.ID).Draw(t, "platforms")
				binary := map[string]string{}
				for _, platform := range platforms {
					binary[platform] = "sha256:" + hex64("binary-"+platform)
				}
				// A local ref may start with "/" or "./", a path's starts
				// (REQ-lock-scalar-values).
				lref := rapid.SampledFrom([]string{"", "/", "./"}).Draw(t, "lrefStart")
				if lref == "" || rapid.Bool().Draw(t, "lrefBody") {
					lref += plain("lref", printable)
				}
				f.Plugins = append(f.Plugins, PluginPin{Ref: lref, Scheme: SchemeLocal, Binary: binary})
			} else {
				pin := PluginPin{Ref: plain("ref", printableNoAt), Scheme: SchemeOCI, Digest: "sha256:" + hex64("pdigest")}
				if rapid.Bool().Draw(t, "hasImageProv") {
					pin.Provenance = Provenance{Type: ProvenanceImageSignature, SAN: plain("psan", printable), Issuer: plain("pissuer", printable)}
				}
				f.Plugins = append(f.Plugins, pin)
			}
		}
		out1, err := Encode(&f)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		// Order independence: shuffle input pins.
		shuffled := File{Modules: slicesClone(f.Modules), Plugins: slicesClone(f.Plugins)}
		for i := len(shuffled.Modules) - 1; i > 0; i-- {
			j := rapid.IntRange(0, i).Draw(t, "j")
			shuffled.Modules[i], shuffled.Modules[j] = shuffled.Modules[j], shuffled.Modules[i]
		}
		out2, err := Encode(&shuffled)
		if err != nil || string(out1) != string(out2) {
			t.Fatalf("emission depends on input order (%v)", err)
		}
		parsed, err := Parse(out1)
		if err != nil {
			t.Fatalf("Parse(Encode): %v\n%s", err, out1)
		}
		want := &File{Modules: slicesClone(f.Modules), Plugins: slicesClone(f.Plugins)}
		sortPins(want)
		if len(want.Modules) == 0 {
			want.Modules = nil
		}
		if len(want.Plugins) == 0 {
			want.Plugins = nil
		}
		if !reflect.DeepEqual(parsed, want) {
			t.Fatalf("Parse(Encode) altered the recorded facts:\n%s\ngot  %+v\nwant %+v", out1, parsed, want)
		}
		out3, err := Encode(parsed)
		if err != nil || string(out1) != string(out3) {
			t.Fatalf("emission is not a fixed point (%v)", err)
		}
	})
}

func slicesClone[T any](s []T) []T {
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// FuzzParse: never panics; success implies re-encodable content whose
// emission reparses to an equal pin set.
func FuzzParse(f *testing.F) {
	f.Add([]byte(goldenEncoded))
	f.Add([]byte("version: 1\nmodules:\n  - path: example.com/c\n    version: v2.0.0\n    provenance:\n      type: git-pinned-key\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      key:\n        kind: openpgp\n        fingerprint: 91EDFEA1C6643EA64EC693516EA5914F2DADE816\n"))
	f.Add([]byte("version: 1\nmodules:\n"))
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		out, err := Encode(parsed)
		if err != nil {
			t.Fatalf("accepted lockfile fails to encode: %v", err)
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("canonical emission fails to parse: %v\n%s", err, out)
		}
		if len(again.Modules) != len(parsed.Modules) || len(again.Plugins) != len(parsed.Plugins) {
			t.Fatalf("emission changed pin counts")
		}
		out2, err := Encode(again)
		if err != nil || string(out) != string(out2) {
			t.Fatalf("emission is not a fixed point")
		}
	})
}

// Analyzer proof for REQ-lock-pins-only: the pin store's exported data
// model is exactly pins — no field can hold a build list, selection, or
// any resolution-derivable fact.
func TestPinsOnlyStructural(t *testing.T) {
	// File carries store methods, so its field set is pinned by reflection
	// in this same analyzer-classified test.
	ft := reflect.TypeFor[File]()
	if ft.NumField() != 2 || ft.Field(0).Name != "Modules" || ft.Field(1).Name != "Plugins" {
		t.Fatalf("File fields changed: pins-only requires exactly Modules and Plugins")
	}
	structural.ExportedData[ModulePin](t,
		structural.FieldOf[string]("Path"),
		structural.FieldOf[string]("Version"),
		structural.FieldOf[string]("Digest"),
		structural.FieldOf[string]("Modfile"),
		structural.FieldOf[Provenance]("Provenance"),
	)
	structural.ExportedData[PluginPin](t,
		structural.FieldOf[string]("Ref"),
		structural.FieldOf[string]("Scheme"),
		structural.FieldOf[string]("Digest"),
		structural.FieldOf[Provenance]("Provenance"),
		structural.FieldOf[map[string]string]("Binary"),
	)
	structural.ExportedData[Provenance](t,
		structural.FieldOf[string]("Type"),
		structural.FieldOf[string]("ObjectFormat"),
		structural.FieldOf[string]("Object"),
		structural.FieldOf[string]("SAN"),
		structural.FieldOf[string]("Issuer"),
		structural.FieldOf[string]("KeyKind"),
		structural.FieldOf[string]("KeyFingerprint"),
	)
}

// Enforcement property: VerifyModule errors exactly when a computed value
// disagrees with a non-empty pinned value, and never mutates the file.
func TestVerifyModuleProperty(t *testing.T) {
	hexes := []rune("0123456789abcdef")
	rapid.Check(t, func(t *rapid.T) {
		hex64 := func(label string) string {
			return string(rapid.SliceOfN(rapid.SampledFrom(hexes), 64, 64).Draw(t, label))
		}
		pin := ModulePin{Path: "example.com/a", Version: "v1.0.0"}
		if rapid.Bool().Draw(t, "hasDigest") {
			pin.Digest = "pb1:" + hex64("digest")
		}
		if rapid.Bool().Draw(t, "hasModfile") {
			pin.Modfile = "sha256:" + hex64("modfile")
		}
		f := &File{Modules: []ModulePin{pin}}
		before, err := Encode(f)
		if err != nil {
			t.Fatal(err)
		}
		computedDigest := "pb1:" + hex64("computedD")
		if rapid.Bool().Draw(t, "digestMatches") && pin.Digest != "" {
			computedDigest = pin.Digest
		}
		computedModfile := "sha256:" + hex64("computedM")
		switch {
		case rapid.Bool().Draw(t, "modfileEmpty"):
			computedModfile = "" // synthesized module: nothing to check
		case rapid.Bool().Draw(t, "modfileMatches") && pin.Modfile != "":
			computedModfile = pin.Modfile
		}
		err = f.VerifyModule(pin.Path, pin.Version, computedDigest, computedModfile)
		wantErr := (pin.Digest != "" && computedDigest != pin.Digest) ||
			(pin.Modfile != "" && computedModfile != "" && computedModfile != pin.Modfile)
		if wantErr != (err != nil) {
			t.Fatalf("verify disagrees with pin comparison: pin=%+v computed=(%s,%s) err=%v", pin, computedDigest, computedModfile, err)
		}
		after, _ := Encode(f)
		if string(before) != string(after) {
			t.Fatalf("VerifyModule mutated the file")
		}
	})
}

// Consistency property: standalone bytes pass exactly when they hash to
// the pin and equal the archive copy.
func TestModfileConsistencyProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		content := []byte(rapid.StringN(0, 64, -1).Draw(t, "content"))
		sum := sha256.Sum256(content)
		goodPin := "sha256:" + hex.EncodeToString(sum[:])
		if err := CheckModfileConsistency(goodPin, content, content); err != nil {
			t.Fatalf("consistent inputs rejected: %v", err)
		}
		if err := CheckModfileConsistency("", content, content); err != nil {
			t.Fatalf("no-pin case rejected: %v", err)
		}
		// Any tampering of the standalone bytes fails against the pin.
		tampered := append([]byte(rapid.StringN(1, 8, -1).Draw(t, "prefix")), content...)
		if err := CheckModfileConsistency(goodPin, tampered, nil); !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("hash mismatch accepted: %v", err)
		}
		// A differing archive copy fails even with a matching pin hash.
		if err := CheckModfileConsistency(goodPin, content, tampered); !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("archive divergence accepted: %v", err)
		}
	})
}

// Adversarial digest/hash forms: boundary bytes around the hex alphabet,
// uppercase, wrong lengths, and valid hex missing its prefix.
func TestHashFormRejections(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	bad := []struct{ name, digest string }{
		{"prefixless valid hex", h64},
		{"uppercase hex", "pb1:" + strings.Repeat("AB", 32)},
		{"colon byte after 9", "pb1:" + strings.Repeat(":", 64)},
		{"backtick byte before a", "pb1:" + strings.Repeat("`", 64)},
		{"g just past f", "pb1:" + strings.Repeat("g", 64)},
		{"63 hex", "pb1:" + h64[:63]},
		{"65 hex", "pb1:" + h64 + "a"},
		{"wrong prefix", "sha256:" + h64},
	}
	for _, tc := range bad {
		f := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0", Digest: tc.digest}}}
		if _, err := Encode(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("digest %s (%q): err = %v, want ErrInvalid", tc.name, tc.digest, err)
		}
	}
	for _, tc := range bad {
		mf := strings.Replace(tc.digest, "pb1:", "sha256:", 1)
		if tc.name == "wrong prefix" {
			mf = "pb1:" + h64
		}
		if tc.name == "prefixless valid hex" {
			mf = tc.digest
		}
		f := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0", Modfile: mf}}}
		if _, err := Encode(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("modfile %s (%q): err = %v, want ErrInvalid", tc.name, mf, err)
		}
	}
}

// A lockfile without plugin pins emits no plugins section at all.
func TestEncodeNoPluginsGolden(t *testing.T) {
	f := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0"}}}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: none\n"
	if string(out) != want {
		t.Fatalf("encoded:\n%s\nwant:\n%s", out, want)
	}
}

// Transition rejections carry branch-specific messages.
func TestProvenanceTransitionMessages(t *testing.T) {
	err := CheckProvenanceTransition(goldenProv, Provenance{})
	if err == nil || !strings.Contains(err.Error(), "would become none") {
		t.Fatalf("none transition: %v", err)
	}
	other := goldenProv
	other.Type = "future-evidence"
	other.Object = goldenProv.Object
	err = CheckProvenanceTransition(goldenProv, other)
	if err == nil || !strings.Contains(err.Error(), "evidence type") {
		t.Fatalf("type transition: %v", err)
	}
	ident := goldenProv
	ident.Issuer = "https://other.example"
	err = CheckProvenanceTransition(goldenProv, ident)
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity transition: %v", err)
	}
}

// Guard-message probes for parse/update/plugin branches.
// Boundary anchors for the plain-scalar value domain: each first-char
// class edge and its outside neighbor, printable-range edges mid-string,
// the null spellings, and the diagnostic naming the offending field.
func TestPlainScalarBoundaries(t *testing.T) {
	accept := []string{"0", "9", "a", "z", "A", "Z", "0x1f", "1e3", "True", "a!b", "a~b", "a@b", "a:b", "nUll", "x'y", `x"y`}
	for _, s := range accept {
		if err := checkPlainScalar("san", s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	reject := []string{"", ":x", "@x", "[x", "_x", "`x", "{x", "~", "~x", "-x", "/x", "./x", ".x", "x:", "a b", "a\x7fb", "x\ny", "café", "null", "Null", "NULL"}
	for _, s := range reject {
		if err := checkPlainScalar("san", s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	// A ref alone admits the path starts a local value is written
	// with, and never "." alone.
	for _, s := range []string{"/x", "/", "./x", "./"} {
		if err := checkPlainScalar("ref", s); err != nil {
			t.Errorf("ref %q rejected: %v", s, err)
		}
	}
	for _, s := range []string{".", ".inf", ".nan", ".x", "-x"} {
		if err := checkPlainScalar("ref", s); err == nil {
			t.Errorf("ref %q accepted", s)
		}
	}
	// The diagnostic carries the field name and the full rule for every
	// kind the callers pass.
	for _, kind := range []string{"version", "san", "issuer"} {
		err := checkPlainScalar(kind, "-x")
		want := kind + ` "-x" is not plain-scalar safe: values start alphanumeric, use printable non-space ASCII, are not a null spelling, and do not end with ":"`
		if err == nil || err.Error() != want {
			t.Errorf("%s diagnostic = %v", kind, err)
		}
	}
	if err := checkPlainScalar("ref", "-x"); err == nil || err.Error() != `ref "-x" is not plain-scalar safe: values start alphanumeric, with "/" or with "./", use printable non-space ASCII, are not a null spelling, and do not end with ":"` {
		t.Errorf("ref diagnostic = %v", err)
	}
}

// Unsafe scalar values must be refused at emission, never rendered: a raw
// newline or YAML-significant shape in a pin fact would inject keys or
// change the fact on the next parse (REQ-lock-canonical-emission).
func TestEncodeRejectsUnsafeScalars(t *testing.T) {
	mod := func(p ModulePin) *File { return &File{Modules: []ModulePin{p}} }
	prov := func(san, issuer string) Provenance {
		return Provenance{Type: "git-signed-tag", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: san, Issuer: issuer}
	}
	cases := []struct {
		name string
		f    *File
		msg  string
	}{
		{"newline in version", mod(ModulePin{Path: "example.com/a", Version: "v1.0.0\nevil: x"}), `version "v1.0.0\nevil: x" is not plain-scalar safe`},
		{"newline in ref", &File{
			Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0"}},
			Plugins: []PluginPin{{Ref: "a\n    x: y", Digest: "sha256:" + strings.Repeat("11", 32)}},
		}, `ref "a\n    x: y" is not plain-scalar safe`},
		{"space in san", mod(ModulePin{Path: "example.com/a", Version: "v1.0.0", Provenance: prov("x #y", "https://i")}), `san "x #y" is not plain-scalar safe`},
		{"trailing colon issuer", mod(ModulePin{Path: "example.com/a", Version: "v1.0.0", Provenance: prov("x@y", "https:")}), `issuer "https:" is not plain-scalar safe`},
		{"leading quote san", mod(ModulePin{Path: "example.com/a", Version: "v1.0.0", Provenance: prov("'q'", "https://i")}), "not plain-scalar safe"},
		{"null version", mod(ModulePin{Path: "example.com/a", Version: "null"}), "not plain-scalar safe"},
		{"null san", mod(ModulePin{Path: "example.com/a", Version: "v1.0.0", Provenance: prov("NULL", "https://i")}), "not plain-scalar safe"},
		{"null ref", &File{
			Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0"}},
			Plugins: []PluginPin{{Ref: "Null", Digest: "sha256:" + strings.Repeat("11", 32)}},
		}, "not plain-scalar safe"},
	}
	for _, tc := range cases {
		out, err := Encode(tc.f)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(fmt.Sprint(err), tc.msg) {
			t.Errorf("%s: err = %v (output %q), want ErrInvalid naming %q", tc.name, err, out, tc.msg)
		}
	}
}

func TestGuardMessages(t *testing.T) {
	// Plugin with empty ref.
	if _, err := Encode(&File{Plugins: []PluginPin{{Digest: "sha256:" + strings.Repeat("11", 32)}}}); err == nil || !strings.Contains(err.Error(), "has no ref") {
		t.Errorf("empty plugin ref: %v", err)
	}
	// Update names the missing pin.
	f := &File{}
	err := f.UpdateModule(ModulePin{Path: "example.com/x", Version: "v1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "no pin for example.com/x@v1.0.0") {
		t.Errorf("update missing: %v", err)
	}
	// AddModule names the existing pin.
	pin := ModulePin{Path: "example.com/a", Version: "v1.0.0"}
	f.AddModule(pin)
	err = f.AddModule(pin)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("re-add: %v", err)
	}
	// Provenance of unsupported YAML shape (sequence).
	_, err = Parse([]byte("version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: [a]\n"))
	if err == nil || !strings.Contains(err.Error(), "neither none nor a record") {
		t.Errorf("provenance sequence: %v", err)
	}
	// Null modules list is an empty lockfile — valid — and re-encodes.
	f2, err := Parse([]byte("version: 1\nmodules:\n"))
	if err != nil {
		t.Fatalf("null modules: %v", err)
	}
	if out, err := Encode(f2); err != nil || string(out) != "version: 1\nmodules:\n" {
		t.Fatalf("empty lockfile encode: %q %v", out, err)
	}
}

// Acceptance variants the wire contract admits: key order, CRLF line
// endings, comments, and escape-free quoting all yield the same recorded
// facts, normalized by re-emission.
func TestAcceptanceVariants(t *testing.T) {
	canonical := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    digest: pb1:" + strings.Repeat("11", 32) + "\n    provenance: none\n"
	variants := map[string]string{
		"key order":           "modules:\n  - version: v1.0.0\n    provenance: none\n    path: example.com/a\n    digest: pb1:" + strings.Repeat("11", 32) + "\nversion: 1\n",
		"crlf":                strings.ReplaceAll(canonical, "\n", "\r\n"),
		"comments":            "version: 1 # one\n# standalone\nmodules:\n  - path: example.com/a\n    version: v1.0.0 # tag\n    digest: pb1:" + strings.Repeat("11", 32) + "\n    provenance: none\n",
		"escape-free quoting": strings.Replace(strings.Replace(canonical, "version: v1.0.0", "\"version\": \"v1.0.0\"", 1), "provenance:", "'provenance':", 1),
	}
	for name, in := range variants {
		f, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		out, err := Encode(f)
		if err != nil || string(out) != canonical {
			t.Errorf("%s: re-emission not canonical (%v):\n%s", name, err, out)
		}
	}
}

// Spellings YAML would type-coerce must be recorded byte-faithfully: the
// raw-scalar decode path covers every free-string fact, so a hex-, bool-,
// or float-looking version or ref survives Parse verbatim.
func TestScalarSpellingsPreserved(t *testing.T) {
	in := "version: 1\nmodules:\n  - path: example.com/a\n    version: 0x1f\n    provenance:\n" +
		"      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n" +
		"      identity:\n        san: 'a\\b'\n        issuer: y\n" +
		"plugins:\n  - ref: True\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n"
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Modules[0].Version; got != "0x1f" {
		t.Errorf("version %q, want the spelling preserved", got)
	}
	if got := f.Modules[0].Provenance.SAN; got != `a\b` {
		t.Errorf("san %q, want the single-quoted backslash literal preserved", got)
	}
	if got := f.Plugins[0].Ref; got != "True" {
		t.Errorf("ref %q, want the spelling preserved", got)
	}
	out, err := Encode(f)
	if err != nil || !strings.Contains(string(out), "version: 0x1f\n") || !strings.Contains(string(out), "ref: True\n") || !strings.Contains(string(out), `san: a\b`+"\n") {
		t.Errorf("re-emission altered spellings (%v):\n%s", err, out)
	}
}

// Anchor cases pinning guard branches no other test's inputs reach.
func TestMutationResidue(t *testing.T) {
	h64 := strings.Repeat("ab", 32)

	// Plugin lookup miss path and second-entry hit.
	f := &File{Plugins: []PluginPin{
		{Ref: "ghcr.io/a/one:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64},
		{Ref: "ghcr.io/a/two:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64},
	}}
	if _, ok := f.Plugin("ghcr.io/a/none:v1", SchemeOCI); ok {
		t.Fatal("missing plugin ref reported found")
	}
	if p, ok := f.Plugin("ghcr.io/a/two:v1", SchemeOCI); !ok || p.Ref != "ghcr.io/a/two:v1" {
		t.Fatalf("second plugin lookup: %+v %v", p, ok)
	}

	// One ref pinned in both schemes coexists: distinct pins, distinct
	// lookups, sorted local-before-oci under one ref (raw-byte order).
	// Input deliberately oci-first: sorted output is local-first, so a
	// dropped scheme tie-break leaves the input order and fails below.
	both := &File{Plugins: []PluginPin{
		{Ref: "protoc-gen-x", Scheme: SchemeOCI, Digest: "sha256:" + h64},
		{Ref: "protoc-gen-x", Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": "sha256:" + h64}},
	}}
	enc, err := Encode(both)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(enc), "scheme: local") > strings.Index(string(enc), "scheme: oci") {
		t.Fatalf("schemes not sorted under one ref:\n%s", enc)
	}
	reparsed, err := Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reparsed.Plugin("protoc-gen-x", SchemeOCI); !ok {
		t.Fatal("oci pin lost beside local")
	}
	if _, ok := reparsed.Plugin("protoc-gen-x", SchemeLocal); !ok {
		t.Fatal("local pin lost beside oci")
	}

	// Two-plugin emission is ref-sorted regardless of input order.
	rev := &File{Plugins: []PluginPin{f.Plugins[1], f.Plugins[0]}}
	a, _ := Encode(f)
	b, err := Encode(rev)
	if err != nil || string(a) != string(b) || !strings.Contains(string(a), "one:v1") {
		t.Fatalf("plugin ordering not canonical (%v)", err)
	}
	if strings.Index(string(a), "one:v1") > strings.Index(string(a), "two:v1") {
		t.Fatalf("plugins not sorted by ref:\n%s", a)
	}

	// Update validates the replacement pin and touches only the exact
	// (path, version) key.
	lf := &File{Modules: []ModulePin{
		{Path: "example.com/a", Version: "v1.0.0", Digest: "pb1:" + h64},
		{Path: "example.com/a", Version: "v2.0.0", Digest: "pb1:" + h64},
	}}
	if err := lf.UpdateModule(ModulePin{Path: "nodot", Version: "v1.0.0"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid update pin: %v", err)
	}
	upd := ModulePin{Path: "example.com/a", Version: "v2.0.0", Digest: "pb1:" + strings.Repeat("cd", 32)}
	if err := lf.UpdateModule(upd); err != nil {
		t.Fatal(err)
	}
	v1, _ := lf.Module("example.com/a", "v1.0.0")
	v2, _ := lf.Module("example.com/a", "v2.0.0")
	if v1.Digest != "pb1:"+h64 || v2.Digest != upd.Digest {
		t.Fatalf("update touched the wrong version: v1=%s v2=%s", v1.Digest, v2.Digest)
	}

	// Plugin pin with an invalid provenance record: an unknown type, and
	// a module's type, which is not a plugin entry's
	// (REQ-lock-provenance-record).
	for _, tc := range []struct {
		p   Provenance
		msg string
	}{
		{Provenance{Type: "pgp", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: "x", Issuer: "y"}, "unknown provenance type"},
		{Provenance{Type: "git-signed-tag", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: "x", Issuer: "y"}, "is not this entry's"},
	} {
		bad := &File{Plugins: []PluginPin{{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64, Provenance: tc.p}}}
		if _, err := Encode(bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Fatalf("plugin bad provenance %q: %v", tc.p.Type, err)
		}
	}
	// A module pin admits no image record.
	imageOnModule := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0", Provenance: Provenance{Type: ProvenanceImageSignature, SAN: "x", Issuer: "y"}}}}
	if _, err := Encode(imageOnModule); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "is not this entry's") {
		t.Fatalf("image record on a module: %v", err)
	}

	// An image-signature record names no signed object
	// (REQ-lock-provenance-record): a git field under it is invalid.
	for _, p := range []Provenance{
		{Type: ProvenanceImageSignature, ObjectFormat: "sha1", SAN: "x", Issuer: "y"},
		{Type: ProvenanceImageSignature, Object: strings.Repeat("ab", 20), SAN: "x", Issuer: "y"},
	} {
		mf := &File{Plugins: []PluginPin{{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64, Provenance: p}}}
		if _, err := Encode(mf); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "names no signed object") {
			t.Fatalf("image record with a git field: %v", err)
		}
	}

	// Identity with exactly one empty half.
	for _, p := range []Provenance{
		{Type: "git-signed-tag", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: "", Issuer: "y"},
		{Type: "git-signed-tag", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: "x", Issuer: ""},
	} {
		mf := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0", Provenance: p}}}
		if _, err := Encode(mf); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "san and issuer") {
			t.Fatalf("half-empty identity: %v", err)
		}
	}
	for _, p := range []Provenance{
		{Type: ProvenanceImageSignature, SAN: "", Issuer: "y"},
		{Type: ProvenanceImageSignature, SAN: "x", Issuer: ""},
	} {
		pf := &File{Plugins: []PluginPin{{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64, Provenance: p}}}
		if _, err := Encode(pf); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "san and issuer") {
			t.Fatalf("half-empty image identity: %v", err)
		}
	}

	// Merge key hidden in the SECOND module entry (recursion must not stop
	// after the first element).
	in := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: none\n  - <<: {path: example.com/b}\n    version: v1.0.0\n    provenance: none\n"
	if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), "merge keys") {
		t.Fatalf("second-entry merge key: %v", err)
	}

	// Provenance record with an unknown extra field.
	in = "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      extra: 1\n      identity:\n        san: x\n        issuer: y\n"
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("provenance extra field: %v", err)
	}

	// A plugin entry with a malformed provenance record fails in Parse.
	in = "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: none\nplugins:\n  - ref: ghcr.io/a/b:v1\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: maybe\n"
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "neither none nor a record") {
		t.Fatalf("plugin bad provenance in parse: %v", err)
	}

	// A plugin entry missing its provenance key fails with the record guard.
	in = "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: none\nplugins:\n  - ref: ghcr.io/a/b:v1\n    digest: sha256:" + strings.Repeat("11", 32) + "\n"
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "missing provenance") {
		t.Fatalf("plugin missing provenance: %v", err)
	}
	// An explicitly null provenance type exercises empty-fragment scalar
	// capture without panicking.
	in = "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type:\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: x\n        issuer: y\n"
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("null provenance type: %v", err)
	}

	// YAML syntax error and wrong-typed version.
	if _, err := Parse([]byte("\t- : :")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("syntax error: %v", err)
	}
	if _, err := Parse([]byte("version: []\n")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("sequence version must fail in decode, not the version check: %v", err)
	}

	// Hex boundary: first char bad with valid remainder; char below '0'.
	for _, d := range []string{"pb1:Z" + h64[:63], "pb1:" + strings.Repeat("/", 64)} {
		mf := &File{Modules: []ModulePin{{Path: "example.com/a", Version: "v1.0.0", Digest: d}}}
		if _, err := Encode(mf); !errors.Is(err, ErrInvalid) {
			t.Fatalf("hex boundary %q accepted", d)
		}
	}

	// Modfile consistency with no pin and tampered bytes is fine — there
	// is nothing pinned to disagree with.
	if err := CheckModfileConsistency("", []byte("anything"), nil); err != nil {
		t.Fatalf("pinless consistency: %v", err)
	}
}

// An anchored entry can smuggle a merge key past checks that inspect only
// mapping keys; no YAML indirection construct may pass the shape walk,
// anywhere in the tree.
func TestForbiddenYAMLConstructs(t *testing.T) {
	cases := []struct{ name, in, msg string }{
		{"anchored entry hiding merge",
			"version: 1\nmodules:\n  - &e\n    <<: &m {provenance: none}\n    path: a.b/x\n    version: v1.0.0\n", "anchors"},
		{"alias value", "version: 1\nmodules: &m\n  - path: a.b/x\n    version: v1.0.0\n    provenance: none\nplugins: *m\n", "anchors"},
		{"tagged scalar", "version: !!int 1\nmodules:\n  - path: a.b/x\n    version: v1.0.0\n    provenance: none\n", "tags"},
		{"plain merge control", "version: 1\nmodules:\n  - <<: {path: a.b/x}\n    version: v1.0.0\n    provenance: none\n", "merge keys"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want %q rejection", tc.name, err, tc.msg)
		}
	}
	// A digits-only object hash is preserved as spelled: forty zeros is a
	// syntactically valid sha1 hex value (no int-coercion detour).
	in := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: \"" + strings.Repeat("0", 40) + "\"\n      identity:\n        san: x\n        issuer: y\n"
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("all-zeros object: %v", err)
	}
	if f.Modules[0].Provenance.Object != strings.Repeat("0", 40) {
		t.Fatalf("object coerced: %q", f.Modules[0].Provenance.Object)
	}
	// Unquoted digits-only object as well.
	in = strings.Replace(in, "\""+strings.Repeat("0", 40)+"\"", strings.Repeat("0", 40), 1)
	f, err = Parse([]byte(in))
	if err != nil || f.Modules[0].Provenance.Object != strings.Repeat("0", 40) {
		t.Fatalf("unquoted all-zeros object: %+v %v", f, err)
	}
	// Single-quoted spelling strips exactly one quote layer.
	in = strings.Replace(in, strings.Repeat("0", 40), "'"+strings.Repeat("0", 40)+"'", 1)
	f, err = Parse([]byte(in))
	if err != nil || f.Modules[0].Provenance.Object != strings.Repeat("0", 40) {
		t.Fatalf("single-quoted object: %+v %v", f, err)
	}
	// Empty-quoted identity halves are empty, hence rejected.
	in2 := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      type: git-signed-tag\n      objectFormat: sha1\n      object: " + strings.Repeat("ab", 20) + "\n      identity:\n        san: ''\n        issuer: y\n"
	if _, err := Parse([]byte(in2)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "san and issuer") {
		t.Fatalf("empty-quoted san: %v", err)
	}
}

// The platform grammar admits exactly <os>/<arch> in lowercase
// alphanumerics — boundary characters on every range edge.
func TestPlatformGrammar(t *testing.T) {
	for _, ok := range []string{"linux/amd64", "linux/386", "a0/z9", "darwin/arm64"} {
		if err := checkPlatform(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "linux", "linux/", "/amd64", "Linux/amd64", "linux/AMD64",
		"linux/amd_64", "linux/amd/64", "linux/amd`", "linux/amd{", "li:nux/a",
	} {
		if err := checkPlatform(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The scheme/field agreement holds on the Encode path too: a
// hand-built pin carrying another scheme's facts never emits.
func TestEncodeSchemeFieldMismatch(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	cases := []struct {
		name string
		pin  PluginPin
		msg  string
	}{
		{"oci with binary", PluginPin{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64,
			Binary: map[string]string{"linux/amd64": "sha256:" + h64}}, "carry no binary"},
		{"local with digest", PluginPin{Ref: "protoc-gen-x", Scheme: SchemeLocal, Digest: "sha256:" + h64,
			Binary: map[string]string{"linux/amd64": "sha256:" + h64}}, "carry no digest"},
		{"local with provenance", PluginPin{Ref: "protoc-gen-x", Scheme: SchemeLocal,
			Binary:     map[string]string{"linux/amd64": "sha256:" + h64},
			Provenance: goldenProv}, "carry no provenance"},
	}
	for _, tc := range cases {
		if _, err := Encode(&File{Plugins: []PluginPin{tc.pin}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want ErrInvalid with %q", tc.name, err, tc.msg)
		}
	}
}

// An oci pin's provenance record — an image-signature record, the
// one a plugin entry admits — survives the parse into the pin.
func TestPluginProvenanceRecordParsed(t *testing.T) {
	want := Provenance{Type: ProvenanceImageSignature, SAN: goldenProv.SAN, Issuer: goldenProv.Issuer}
	in := "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance: none\n" +
		"plugins:\n  - ref: ghcr.io/a/b:v1\n    scheme: oci\n    digest: sha256:" + strings.Repeat("11", 32) + "\n" +
		"    provenance:\n      type: image-signature\n" +
		"      identity:\n        san: " + goldenProv.SAN + "\n        issuer: " + goldenProv.Issuer + "\n"
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := f.Plugin("ghcr.io/a/b:v1", SchemeOCI)
	if !ok || p.Provenance != want {
		t.Fatalf("plugin provenance lost: %+v", p.Provenance)
	}
}

// AddPlugin guards first use per (ref, scheme): a duplicate pair is
// refused, a different scheme under the same ref is not, and an
// invalid pin never lands (REQ-lock-first-use).
func TestAddPlugin(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	f := &File{}
	oci := PluginPin{Ref: "ghcr.io/a/b:v1", Scheme: SchemeOCI, Digest: "sha256:" + h64}
	if err := f.AddPlugin(oci); err != nil {
		t.Fatal(err)
	}
	if err := f.AddPlugin(oci); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("duplicate pair: %v", err)
	}
	local := PluginPin{Ref: "ghcr.io/a/b:v1", Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": "sha256:" + h64}}
	if err := f.AddPlugin(local); err != nil {
		t.Fatalf("same ref, other scheme: %v", err)
	}
	if err := f.AddPlugin(PluginPin{Ref: "x", Scheme: "remote"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid pin: %v", err)
	}
	if len(f.Plugins) != 2 {
		t.Fatalf("plugins = %d", len(f.Plugins))
	}
}

// A local pin gains a platform on that platform's first use, keeps a
// matching hash as it is, refuses a differing one naming both, and
// is never created by the platform write itself.
func TestSetPluginBinary(t *testing.T) {
	h1, h2 := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	f := &File{}
	if err := f.SetPluginBinary("protoc-gen-x", "linux/amd64", h1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no pin: %v", err)
	}
	if err := f.AddPlugin(PluginPin{Ref: "protoc-gen-x", Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": h1}}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetPluginBinary("protoc-gen-x", "darwin/arm64", h2); err != nil {
		t.Fatal(err)
	}
	if err := f.SetPluginBinary("protoc-gen-x", "linux/amd64", h1); err != nil {
		t.Fatalf("same hash: %v", err)
	}
	err := f.SetPluginBinary("protoc-gen-x", "linux/amd64", h2)
	if !errors.Is(err, ErrPinMismatch) || !strings.Contains(err.Error(), h1) || !strings.Contains(err.Error(), h2) {
		t.Fatalf("differing hash: %v", err)
	}
	if err := f.SetPluginBinary("protoc-gen-x", "bad", h1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad platform: %v", err)
	}
	p, _ := f.Plugin("protoc-gen-x", SchemeLocal)
	if len(p.Binary) != 2 || p.Binary["darwin/arm64"] != h2 {
		t.Fatalf("pin = %+v", p)
	}
}

// A ref may start with "/" or "./" — a local plugin's path as
// written — and still emits as a plain scalar re-parsing to itself;
// "." alone, the float spellings' lead, does not.
func TestPluginRefPathStarts(t *testing.T) {
	h := "sha256:" + strings.Repeat("a", 64)
	for _, ref := range []string{"/opt/protoc-gen-x", "./tools/gen", "tools/gen"} {
		f := &File{}
		if err := f.AddPlugin(PluginPin{Ref: ref, Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": h}}); err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		out, err := Encode(f)
		if err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		back, err := Parse(out)
		if err != nil {
			t.Fatalf("%q: re-parse: %v\n%s", ref, err, out)
		}
		if got, _ := back.Plugin(ref, SchemeLocal); got.Ref != ref {
			t.Fatalf("%q re-parsed as %q", ref, got.Ref)
		}
	}
	for _, ref := range []string{".", ".inf", ".x", "-x"} {
		if err := (&File{}).AddPlugin(PluginPin{Ref: ref, Scheme: SchemeLocal, Binary: map[string]string{"linux/amd64": h}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted: %v", ref, err)
		}
	}
}
