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

func goldenFile() *File {
	return &File{
		Modules: []ModulePin{
			{Path: "example.com/b", Version: "v0.1.0", Digest: "pb1:" + strings.Repeat("22", 32), Provenance: Provenance{}},
			{Path: "example.com/a", Version: "v1.2.3", Digest: "pb1:" + strings.Repeat("11", 32), Modfile: "sha256:" + strings.Repeat("33", 32), Provenance: goldenProv},
			{Path: "example.com/a", Version: "v1.0.0", Modfile: "sha256:" + strings.Repeat("44", 32), Provenance: Provenance{}},
		},
		Plugins: []PluginPin{
			{Ref: "ghcr.io/acme/protoc-gen-x:v2", Digest: "sha256:" + strings.Repeat("55", 32), Provenance: Provenance{}},
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
plugins:
  - ref: ghcr.io/acme/protoc-gen-x:v2
    digest: sha256:` + "5555555555555555555555555555555555555555555555555555555555555555" + `
    provenance: none
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

func TestParseGoldenRoundTrip(t *testing.T) {
	f, err := Parse([]byte(goldenEncoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Modules) != 3 || len(f.Plugins) != 1 {
		t.Fatalf("parsed %d modules, %d plugins", len(f.Modules), len(f.Plugins))
	}
	pin, ok := f.Module("example.com/a", "v1.2.3")
	if !ok || pin.Provenance != goldenProv {
		t.Fatalf("rich pin lost: %+v", pin)
	}
	if _, ok := f.Plugin("ghcr.io/acme/protoc-gen-x:v2"); !ok {
		t.Fatalf("plugin pin lost")
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
		{"null key defeating strict decode", "version: 1\nmodules:\n  - path: example.com/a\n    version: v1.0.0\n    provenance:\n      null: x\n", "mapping keys are strings"},
		{"null key at top level", "version: 1\nnull: x\nmodules:\n" + mod, "mapping keys are strings"},
		{"plugin digest in ref", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b@sha256:" + strings.Repeat("11", 32) + "\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "carries a digest"},
		{"plugin bad digest", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    digest: pb1:" + strings.Repeat("11", 32) + "\n    provenance: none\n", "not sha256:"},
		{"duplicate plugin pin", "version: 1\nmodules:\n" + mod + "plugins:\n  - ref: ghcr.io/a/b:v1\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n  - ref: ghcr.io/a/b:v1\n    digest: sha256:" + strings.Repeat("22", 32) + "\n    provenance: none\n", "duplicate plugin pin"},
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
				pin.Provenance = Provenance{
					Type: "git-signed-tag", ObjectFormat: "sha256", Object: hex64("obj"),
					SAN:    plain("san", printable),
					Issuer: plain("issuer", printable),
				}
			}
			f.Modules = append(f.Modules, pin)
		}
		if rapid.Bool().Draw(t, "hasPlugin") {
			f.Plugins = append(f.Plugins, PluginPin{Ref: plain("ref", printableNoAt), Digest: "sha256:" + hex64("pdigest")})
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
		structural.FieldOf[string]("Digest"),
		structural.FieldOf[Provenance]("Provenance"),
	)
	structural.ExportedData[Provenance](t,
		structural.FieldOf[string]("Type"),
		structural.FieldOf[string]("ObjectFormat"),
		structural.FieldOf[string]("Object"),
		structural.FieldOf[string]("SAN"),
		structural.FieldOf[string]("Issuer"),
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
	reject := []string{"", ":x", "@x", "[x", "_x", "`x", "{x", "~", "~x", "-x", "/x", "x:", "a b", "a\x7fb", "x\ny", "café", "null", "Null", "NULL"}
	for _, s := range reject {
		if err := checkPlainScalar("san", s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	// The diagnostic carries the field name and the full rule for every
	// kind the callers pass.
	for _, kind := range []string{"version", "san", "issuer", "ref"} {
		err := checkPlainScalar(kind, "-x")
		want := kind + ` "-x" is not plain-scalar safe: values start alphanumeric, use printable non-space ASCII, are not a null spelling, and do not end with ":"`
		if err == nil || err.Error() != want {
			t.Errorf("%s diagnostic = %v", kind, err)
		}
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
		"plugins:\n  - ref: True\n    digest: sha256:" + strings.Repeat("11", 32) + "\n    provenance: none\n"
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
		{Ref: "ghcr.io/a/one:v1", Digest: "sha256:" + h64},
		{Ref: "ghcr.io/a/two:v1", Digest: "sha256:" + h64},
	}}
	if _, ok := f.Plugin("ghcr.io/a/none:v1"); ok {
		t.Fatal("missing plugin ref reported found")
	}
	if p, ok := f.Plugin("ghcr.io/a/two:v1"); !ok || p.Ref != "ghcr.io/a/two:v1" {
		t.Fatalf("second plugin lookup: %+v %v", p, ok)
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

	// Plugin pin with an invalid provenance record.
	bad := &File{Plugins: []PluginPin{{Ref: "ghcr.io/a/b:v1", Digest: "sha256:" + h64,
		Provenance: Provenance{Type: "pgp", ObjectFormat: "sha1", Object: strings.Repeat("ab", 20), SAN: "x", Issuer: "y"}}}}
	if _, err := Encode(bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "unknown provenance type") {
		t.Fatalf("plugin bad provenance: %v", err)
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
