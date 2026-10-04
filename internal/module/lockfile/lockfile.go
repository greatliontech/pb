// Package lockfile parses, validates, and canonically emits pb.lock — the
// pin store: content identity and provenance facts for every (module path,
// version) a resolution has consulted, plus plugin reference pins. It
// records only pins (REQ-lock-pins-only): version selection is recomputed
// from module files, and nothing derivable by re-running resolution is
// stored — the data model has no fields for it.
//
// Parsing goes through the YAML AST, as module-file parsing does, so
// the accepted surface is the schema's; emission is hand-rolled because
// regeneration must be byte-identical (REQ-lock-canonical-emission).
package lockfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/plugin"
)

// ErrInvalid is wrapped by every lockfile rejection.
var ErrInvalid = errors.New("invalid lockfile")

// ErrPinMismatch is wrapped when a fetched artifact disagrees with its pin
// (REQ-lock-digest-enforcement) or when a pin addition conflicts with an
// existing pin (REQ-lock-first-use).
var ErrPinMismatch = errors.New("lockfile pin mismatch")

// ErrProvenanceDowngrade is wrapped when a non-explicit operation would
// weaken or alter a verified provenance record
// (REQ-lock-no-silent-downgrade).
var ErrProvenanceDowngrade = errors.New("provenance downgrade")

// The evidence types a provenance record names
// (REQ-lock-provenance-record).
const (
	// ProvenanceGitSignedTag is a signed git tag over the module's
	// commit: the record carries the signed object.
	ProvenanceGitSignedTag = "git-signed-tag"
	// ProvenanceImageSignature is a sigstore signature over the
	// plugin image's digest: the record carries the identity alone,
	// the entry's digest being what was signed.
	ProvenanceImageSignature = "image-signature"
	// ProvenanceGitPinnedKey is a signed git tag over the module's
	// commit accepted under a trust-policy rule naming pinned keys:
	// the record carries the signed object and, in place of an
	// identity, the kind and fingerprint of the pinned key that
	// verified it (REQ-lock-pinned-key-record).
	ProvenanceGitPinnedKey = "git-pinned-key"
)

// Provenance is a lockfile provenance record (REQ-lock-provenance-record,
// REQ-lock-pinned-key-record). The zero value is the literal `none`.
// A record names a verified Fulcio identity (SAN and Issuer) or a
// pinned key (KeyKind and KeyFingerprint), as its type says, never
// both.
type Provenance struct {
	Type           string // ProvenanceGitSignedTag, ProvenanceGitPinnedKey or ProvenanceImageSignature
	ObjectFormat   string // git-signed-tag, git-pinned-key: "sha1" or "sha256"
	Object         string // git-signed-tag, git-pinned-key: hex git hash of the signed object
	Subtree        string // git-signed-tag, git-pinned-key: the module's directory in the repository the tag bound it under, "" at the root
	Repo           string // git-signed-tag: the origin's HTTPS repository the record was accepted under, the origin default re-derives from
	SAN            string // git-signed-tag, image-signature
	Issuer         string // git-signed-tag, image-signature
	KeyKind        string // git-pinned-key: "openpgp" or "ssh"
	KeyFingerprint string // git-pinned-key: the pinned key's fingerprint as its kind spells it
}

// NamesKey reports whether a record names a pinned key in place of an
// identity (REQ-lock-pinned-key-record): what vouched for it is its
// key's kind and fingerprint, not a SAN and issuer. A function rather
// than a method: the record is pins-only data (REQ-lock-pins-only).
func NamesKey(p Provenance) bool { return recordShapes[p.Type].namesKey }

// recordShape is what a record of a type names beside its type
// (REQ-lock-provenance-record, REQ-lock-pinned-key-record): a signed
// git object or none, and a key or an identity. The one table the
// check, the writer and the reader read, so no two of them re-derive
// the rule.
type recordShape struct {
	signsObject bool
	namesKey    bool
	namesRepo   bool
	article     string
}

var recordShapes = map[string]recordShape{
	ProvenanceGitSignedTag:   {signsObject: true, namesRepo: true, article: "a"},
	ProvenanceGitPinnedKey:   {signsObject: true, namesKey: true, article: "a"},
	ProvenanceImageSignature: {article: "an"},
}

// Fingerprint spellings by key kind (provenance.md
// REQ-prov-pinned-keys-schema): an OpenPGP primary key's uppercase
// hex, forty digits for a version 4 key and sixty-four for a version
// 6 one; an SSH key's OpenSSH SHA256 form — the prefix and forty-three
// unpadded base64 digits.
var fingerprintSpellings = map[string]*regexp.Regexp{
	"openpgp": regexp.MustCompile(`^(?:[0-9A-F]{40}|[0-9A-F]{64})$`),
	"ssh":     regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`),
}

// ModulePin is one module entry (REQ-lock-entry).
type ModulePin struct {
	Path       string
	Version    string
	Digest     string // "pb1:" + 64 hex; empty when the archive has not been fetched
	Modfile    string // "sha256:" + 64 hex; empty when the module declares no module file
	Provenance Provenance
}

// SchemeOCI is the one plugin identity scheme the lockfile records
// (REQ-lock-plugin-entry), named from its home for callers already
// reaching it through this package; a local plugin, a host binary,
// has no entry (plugin-execution.md, "Local binaries").
const SchemeOCI = plugin.SchemeOCI

// PluginPin is one plugin entry (REQ-lock-plugin-entry): identity facts
// for the (ref, scheme) pair — the manifest-list digest the reference
// resolved to and the image signature's provenance record.
type PluginPin struct {
	Ref        string // the reference as written in generation configuration, without a digest: the pin's key, the digest its identity
	Scheme     string // SchemeOCI — stated, never inferred from fields
	Digest     string // "sha256:" + 64 hex manifest-list digest
	Provenance Provenance
}

// File is a parsed lockfile: pins only. Rulesets are module pins of
// their own list (REQ-lock-ruleset-entry): the same facts under the
// same rules, for the pairs ruleset imports name, never members of
// the build list.
type File struct {
	Modules  []ModulePin
	Rulesets []ModulePin
	Plugins  []PluginPin
}

// Pins is one of the lockfile's two module-pin lists, the modules' or
// the rulesets' (REQ-lock-format, REQ-lock-ruleset-entry): a pair is
// pinned in the list its reader keeps, a pair both a declaration and
// an import name in both, each by its own.
type Pins struct {
	f        *File
	rulesets bool
}

// ModulePins is the build list's pin list.
func (f *File) ModulePins() Pins { return Pins{f, false} }

// RulesetPins is the ruleset imports' pin list.
func (f *File) RulesetPins() Pins { return Pins{f, true} }

// Other is the lockfile's other list: the rulesets' for the modules'
// and the modules' for the rulesets'.
func (p Pins) Other() Pins { return Pins{p.f, !p.rulesets} }

// Name is the list's name, "module" or "ruleset".
func (p Pins) Name() string {
	if p.rulesets {
		return "ruleset"
	}
	return "module"
}

// All is the list's pins as recorded, in their recorded order.
func (p Pins) All() []ModulePin { return slices.Clone(*p.list()) }

func (p Pins) list() *[]ModulePin {
	if p.rulesets {
		return &p.f.Rulesets
	}
	return &p.f.Modules
}

// Module returns the list's pin for (path, version).
func (p Pins) Module(path, version string) (ModulePin, bool) {
	for _, m := range *p.list() {
		if m.Path == path && m.Version == version {
			return m, true
		}
	}
	return ModulePin{}, false
}

// Add records a first-use pin in the list (REQ-lock-first-use): it is
// an error if a pin for (path, version) already exists — pins are only
// added or explicitly updated, never silently rewritten.
func (p Pins) Add(pin ModulePin) error {
	if err := checkModulePin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, exists := p.Module(pin.Path, pin.Version); exists {
		return fmt.Errorf("%w: pin for %s@%s already exists", ErrPinMismatch, pin.Path, pin.Version)
	}
	*p.list() = append(*p.list(), pin)
	return nil
}

// Update is the list's explicit update path
// (REQ-lock-no-silent-downgrade): the only operation that may change
// an existing pin, including weakening or altering its provenance
// record.
func (p Pins) Update(pin ModulePin) error {
	if err := checkModulePin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	l := *p.list()
	for i, m := range l {
		if m.Path == pin.Path && m.Version == pin.Version {
			l[i] = pin
			return nil
		}
	}
	return fmt.Errorf("%w: no pin for %s@%s to update", ErrPinMismatch, pin.Path, pin.Version)
}

// Verify enforces a fetched artifact against the list's pin
// (REQ-lock-digest-enforcement), as File.VerifyModule says.
func (p Pins) Verify(path, version, computedDigest, computedModfile string) error {
	pin, ok := p.Module(path, version)
	if !ok {
		return fmt.Errorf("%w: no pin for %s@%s", ErrPinMismatch, path, version)
	}
	if pin.Digest != "" && pin.Digest != computedDigest {
		return fmt.Errorf("%w: %s@%s digest: expected %s, computed %s", ErrPinMismatch, path, version, pin.Digest, computedDigest)
	}
	if pin.Modfile != "" && computedModfile != "" && pin.Modfile != computedModfile {
		return fmt.Errorf("%w: %s@%s modfile: expected %s, computed %s", ErrPinMismatch, path, version, pin.Modfile, computedModfile)
	}
	return nil
}

func hexOK(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkPlainScalar bounds a free-string pin fact to values whose unquoted
// emission re-parses as the same plain YAML scalar, keeping Encode a fixed
// point of Parse (REQ-lock-canonical-emission): printable non-space ASCII,
// leading alphanumeric (no YAML indicator or quote interpretation), no
// trailing ':' (which would turn the value into a nested mapping), and not
// a YAML null spelling (which a parser resolves to null, skipping the
// value decode entirely).
func checkPlainScalar(kind, s string) error {
	bad := s == "" || s[len(s)-1] == ':' || s == "null" || s == "Null" || s == "NULL"
	if !bad {
		c := s[0]
		bad = !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
	}
	for i := 0; !bad && i < len(s); i++ {
		bad = s[i] < '!' || s[i] > '~'
	}
	if bad {
		return fmt.Errorf("%s %q is not plain-scalar safe: values start alphanumeric, use printable non-space ASCII, are not a null spelling, and do not end with %q", kind, s, ":")
	}
	return nil
}

// checkHashRef bounds a hash-reference fact to prefix plus 64 lowercase
// hex digits ("pb1:" module digests, "sha256:" module-file hashes and
// plugin digests).
func checkHashRef(prefix, d string) error {
	rest, ok := strings.CutPrefix(d, prefix)
	if !ok || !hexOK(rest, 64) {
		return fmt.Errorf("%q is not %s plus 64 lowercase hex digits", d, prefix)
	}
	return nil
}

// checkProvenance validates a record against the evidence types its
// entry kind admits (REQ-lock-provenance-record,
// REQ-lock-pinned-key-record): a module entry a git-signed-tag or a
// git-pinned-key record, a plugin entry an image-signature one.
func checkProvenance(p Provenance, admitted ...string) error {
	if p == (Provenance{}) {
		return nil
	}
	shape, known := recordShapes[p.Type]
	if !known {
		return fmt.Errorf("unknown provenance type %q", p.Type)
	}
	if !slices.Contains(admitted, p.Type) {
		return fmt.Errorf("provenance type %q is not this entry's (%s)", p.Type, strings.Join(admitted, " or "))
	}
	if shape.signsObject {
		var hexLen int
		switch p.ObjectFormat {
		case "sha1":
			hexLen = 40
		case "sha256":
			hexLen = 64
		default:
			return fmt.Errorf("unknown object format %q", p.ObjectFormat)
		}
		if !hexOK(p.Object, hexLen) {
			return fmt.Errorf("object %q is not %d lowercase hex digits", p.Object, hexLen)
		}
		if err := module.ValidateSubtree(p.Subtree); err != nil {
			return err
		}
	} else if p.ObjectFormat != "" || p.Object != "" || p.Subtree != "" {
		return fmt.Errorf("%s %s record names no signed object", shape.article, p.Type)
	}
	if shape.namesKey {
		if p.SAN != "" || p.Issuer != "" {
			return fmt.Errorf("%s %s record names a key, not an identity", shape.article, p.Type)
		}
		if p.KeyKind == "" && p.KeyFingerprint == "" {
			return errors.New("key needs a kind and a fingerprint")
		}
		spelling, ok := fingerprintSpellings[p.KeyKind]
		if !ok {
			return fmt.Errorf("unknown key kind %q", p.KeyKind)
		}
		if p.KeyFingerprint == "" {
			return errors.New("key needs a fingerprint")
		}
		if !spelling.MatchString(p.KeyFingerprint) {
			return fmt.Errorf("fingerprint %q is not spelled as %s spells one", p.KeyFingerprint, p.KeyKind)
		}
		return nil
	}
	if p.KeyKind != "" || p.KeyFingerprint != "" {
		return fmt.Errorf("%s %s record names an identity, not a key", shape.article, p.Type)
	}
	if p.SAN == "" || p.Issuer == "" {
		return errors.New("identity needs both san and issuer")
	}
	switch {
	case shape.namesRepo && p.Repo == "":
		return fmt.Errorf("%s %s record names no repo: the repository it was accepted under, which a record written before it was recorded lacks; set this entry's provenance to none and run pb dep update <path>@<version> for the pair, or remove the entry and run pb dep download", shape.article, p.Type)
	case shape.namesRepo:
		if err := checkPlainScalar("repo", p.Repo); err != nil {
			return err
		}
	case p.Repo != "":
		return fmt.Errorf("%s %s record names no repository", shape.article, p.Type)
	}
	if err := checkPlainScalar("san", p.SAN); err != nil {
		return err
	}
	if err := checkPlainScalar("issuer", p.Issuer); err != nil {
		return err
	}
	return nil
}

func checkModulePin(m ModulePin) error {
	if err := module.ValidatePath(m.Path); err != nil {
		return err
	}
	if m.Version == "" {
		return fmt.Errorf("module %q has no version", m.Path)
	}
	if err := checkPlainScalar("version", m.Version); err != nil {
		return fmt.Errorf("module %q: %v", m.Path, err)
	}
	if m.Digest != "" {
		if err := checkHashRef("pb1:", m.Digest); err != nil {
			return fmt.Errorf("digest: %v", err)
		}
	}
	if m.Modfile != "" {
		if err := checkHashRef("sha256:", m.Modfile); err != nil {
			return fmt.Errorf("modfile: %v", err)
		}
	}
	if err := checkProvenance(m.Provenance, ProvenanceGitSignedTag, ProvenanceGitPinnedKey); err != nil {
		return fmt.Errorf("module %q: %v", m.Path, err)
	}
	return nil
}

func checkPluginPin(p PluginPin) error {
	if p.Scheme == plugin.SchemeLocal {
		// A host binary has no identity pb could resolve, and the
		// lockfile records none, whatever the entry's other facts.
		return fmt.Errorf("plugin %q: a local plugin has no lockfile entry; remove it", p.Ref)
	}
	if p.Ref == "" {
		return errors.New("plugin entry has no ref")
	}
	if err := checkPlainScalar("ref", p.Ref); err != nil {
		return err
	}
	switch p.Scheme {
	case SchemeOCI:
		if strings.Contains(p.Ref, "@") {
			return fmt.Errorf("plugin ref %q carries a digest; refs are pinned by the digest field", p.Ref)
		}
		if err := checkHashRef("sha256:", p.Digest); err != nil {
			return fmt.Errorf("plugin %q: %v", p.Ref, err)
		}
		if err := checkProvenance(p.Provenance, ProvenanceImageSignature); err != nil {
			return fmt.Errorf("plugin %q: %v", p.Ref, err)
		}
	default:
		return fmt.Errorf("plugin %q: unknown scheme %q", p.Ref, p.Scheme)
	}
	return nil
}

// validate checks a File's content; Parse accepts and Encode emits exactly
// the files that pass it. Entry uniqueness: one pin per (path, version) and
// one per ref.
func validate(f *File) error {
	for _, l := range []struct {
		what string
		pins []ModulePin
	}{{"module", f.Modules}, {"ruleset", f.Rulesets}} {
		seen := make(map[[2]string]struct{}, len(l.pins))
		for _, m := range l.pins {
			if err := checkModulePin(m); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			k := [2]string{m.Path, m.Version}
			if _, dup := seen[k]; dup {
				return fmt.Errorf("%w: duplicate %s pin %s@%s", ErrInvalid, l.what, m.Path, m.Version)
			}
			seen[k] = struct{}{}
		}
	}
	// One pair names one content: a pair pinned in both lists carries
	// one digest (REQ-lock-ruleset-entry).
	moduleDigest := make(map[[2]string]string, len(f.Modules))
	for _, m := range f.Modules {
		moduleDigest[[2]string{m.Path, m.Version}] = m.Digest
	}
	for _, r := range f.Rulesets {
		if d, ok := moduleDigest[[2]string{r.Path, r.Version}]; ok && d != "" && r.Digest != "" && d != r.Digest {
			return fmt.Errorf("%w: %s@%s pinned as a module at %s and as a ruleset at %s: one pair names one content", ErrInvalid, r.Path, r.Version, d, r.Digest)
		}
	}
	seenP := make(map[[2]string]struct{}, len(f.Plugins))
	for _, p := range f.Plugins {
		if err := checkPluginPin(p); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		k := [2]string{p.Ref, p.Scheme}
		if _, dup := seenP[k]; dup {
			return fmt.Errorf("%w: duplicate plugin pin %q (%s)", ErrInvalid, p.Ref, p.Scheme)
		}
		seenP[k] = struct{}{}
	}
	return nil
}

func sortPins(f *File) {
	byPair := func(a, b ModulePin) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	}
	slices.SortFunc(f.Modules, byPair)
	slices.SortFunc(f.Rulesets, byPair)
	slices.SortFunc(f.Plugins, func(a, b PluginPin) int {
		if c := strings.Compare(a.Ref, b.Ref); c != 0 {
			return c
		}
		return strings.Compare(a.Scheme, b.Scheme)
	})
}

// Encode renders the lockfile canonically (REQ-lock-canonical-emission,
// REQ-lock-format): version 1, modules sorted by (path, version),
// rulesets so sorted where any are pinned, plugins sorted by ref, fixed key order, two-space indent, block style, LF,
// every value a plain scalar — the domain REQ-lock-scalar-values bounds
// is what the reader takes raw, so no value is quoted — and the
// rendering held to its reading. Emission is a pure function of the
// recorded facts.
func Encode(f *File) ([]byte, error) {
	if err := validate(f); err != nil {
		return nil, err
	}
	c := &File{Modules: slices.Clone(f.Modules), Rulesets: slices.Clone(f.Rulesets), Plugins: slices.Clone(f.Plugins)}
	sortPins(c)
	return contractfile.Emit(func(w *contractfile.Writer) {
		w.Literal("version", "1")
		modulePins := func(key string, pins []ModulePin) {
			w.Sequence(key, len(pins), func(i int) {
				m := pins[i]
				w.Literal("path", m.Path)
				w.Literal("version", m.Version)
				if m.Digest != "" {
					w.Literal("digest", m.Digest)
				}
				if m.Modfile != "" {
					w.Literal("modfile", m.Modfile)
				}
				writeProvenance(w, m.Provenance)
			})
		}
		modulePins("modules", c.Modules)
		if len(c.Rulesets) > 0 {
			modulePins("rulesets", c.Rulesets)
		}
		if len(c.Plugins) > 0 {
			w.Sequence("plugins", len(c.Plugins), func(i int) {
				p := c.Plugins[i]
				w.Literal("ref", p.Ref)
				w.Literal("scheme", p.Scheme)
				w.Literal("digest", p.Digest)
				writeProvenance(w, p.Provenance)
			})
		}
	}, func(out []byte) (File, error) {
		again, err := Parse(out)
		if err != nil {
			return File{}, err
		}
		sortPins(again)
		return pinsOf(again), nil
	}, pinsOf(c), func(a, b File) bool { return reflect.DeepEqual(a, b) }, ErrInvalid)
}

// pinsOf is a file's pins as recorded facts, an absent list and an
// empty one alike — a file may pin no module, no ruleset and no plugin.
func pinsOf(f *File) File {
	var out File
	if len(f.Modules) > 0 {
		out.Modules = f.Modules
	}
	if len(f.Rulesets) > 0 {
		out.Rulesets = f.Rulesets
	}
	if len(f.Plugins) > 0 {
		out.Plugins = f.Plugins
	}
	return out
}

// writeProvenance writes a pin's provenance record: `none` where it
// has none, else its type and, for a signed tag, the object, then the
// identity — or, for a pinned key, the key.
func writeProvenance(w *contractfile.Writer, p Provenance) {
	if p == (Provenance{}) {
		w.Literal("provenance", "none")
		return
	}
	shape := recordShapes[p.Type]
	w.Mapping("provenance", func() {
		w.Literal("type", p.Type)
		if shape.signsObject {
			w.Literal("objectFormat", p.ObjectFormat)
			w.Literal("object", p.Object)
			if p.Subtree != "" {
				// A subtree is a module path's segments, which YAML may
				// read otherwise unquoted (`-`, `~`, `null`): spelled as
				// Spell spells a scalar, quoted where plain would not
				// read back as itself.
				w.Scalar("subtree", p.Subtree)
			}
		}
		if shape.namesRepo {
			w.Literal("repo", p.Repo)
		}
		if shape.namesKey {
			w.Mapping("key", func() {
				w.Literal("kind", p.KeyKind)
				w.Literal("fingerprint", p.KeyFingerprint)
			})
			return
		}
		w.Mapping("identity", func() {
			w.Literal("san", p.SAN)
			w.Literal("issuer", p.Issuer)
		})
	})
}

// rawScalar captures a scalar's exact spelling (one matching quote layer
// stripped) instead of goccy's typed interpretation: a digits-only object
// hash must not collapse leading zeros, and identity values are preserved
// byte-faithfully.
type rawScalar string

func (r *rawScalar) UnmarshalYAML(b []byte) error {
	s := strings.TrimSpace(string(b))
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		inner := s[1 : len(s)-1]
		// One layer of escape-free quoting is spelling, not content; with
		// an escape inside, the stripped bytes would differ from the YAML
		// value — reject rather than record a misread.
		if strings.ContainsRune(inner, rune(s[0])) || (s[0] == '"' && strings.Contains(inner, `\`)) {
			return fmt.Errorf("quoted scalar %s contains escapes; only escape-free quoting is accepted", s)
		}
		s = inner
	}
	*r = rawScalar(s)
	return nil
}

type rawIdentity struct {
	SAN    rawScalar `yaml:"san"`
	Issuer rawScalar `yaml:"issuer"`
}

type rawKey struct {
	Kind        rawScalar `yaml:"kind"`
	Fingerprint rawScalar `yaml:"fingerprint"`
}

// rawProvenance's optional parts are pointers, so a part written and
// empty (`key: {}`, `objectFormat: ""`) reads as written; the node
// records the keys written beside, so a part written with no value
// does too. A record naming a part its type does not is invalid
// whatever the part holds.
type rawProvenance struct {
	Type         rawScalar    `yaml:"type"`
	ObjectFormat *rawScalar   `yaml:"objectFormat"`
	Object       *rawScalar   `yaml:"object"`
	Subtree      *rawScalar   `yaml:"subtree"`
	Repo         *rawScalar   `yaml:"repo"`
	Identity     *rawIdentity `yaml:"identity"`
	Key          *rawKey      `yaml:"key"`
}

// provNode captures the provenance value's raw YAML fragment
// (BytesUnmarshaler), so scalars keep their exact spelling — no
// map[string]any type-coercion detour: a digits-only object hash stays a
// string, and identity values are preserved byte-faithfully.
type provNode struct {
	set     bool
	rec     rawProvenance
	present map[string]bool // the record's keys as written, a null value included
}

func (p *provNode) UnmarshalYAML(b []byte) error {
	p.set = true
	// The spelled none, quoted or not — one escape-free quote layer is
	// spelling, as for every scalar (REQ-lock-acceptance). The zero
	// record is the none record; no separate flag needed.
	var scalar rawScalar
	if err := scalar.UnmarshalYAML(b); err == nil && scalar == "none" {
		return nil
	}
	if err := yaml.UnmarshalWithOptions(b, &p.rec, yaml.Strict()); err != nil {
		return fmt.Errorf("provenance is neither none nor a record: %v", err)
	}
	// Which parts the record writes, whatever they hold: a part written
	// with no value (`key:`) is written, and the typed decode leaves
	// its pointer nil.
	var keys map[string]any
	if err := yaml.Unmarshal(b, &keys); err != nil {
		return fmt.Errorf("provenance is neither none nor a record: %v", err)
	}
	p.present = map[string]bool{}
	for k := range keys {
		p.present[k] = true
	}
	// A record names its type: a record mapping naming none — empty,
	// or holding parts alone — is a mangled lockfile, and reading it
	// as the spelled none would erase provenance silently.
	if len(p.present) == 0 {
		return errors.New("provenance record is empty")
	}
	if p.rec.Type == "" {
		return errors.New("provenance record names no type")
	}
	return nil
}

// record is the provenance record the node holds, its parts each
// present only where its type names them (REQ-lock-provenance-record):
// a part written and empty is still written.
func (p *provNode) record() (Provenance, error) {
	if !p.set {
		return Provenance{}, errors.New("missing provenance")
	}
	r := p.rec
	rec := Provenance{Type: string(r.Type)}
	if shape, known := recordShapes[rec.Type]; known {
		if !shape.signsObject && (p.present["objectFormat"] || p.present["object"] || p.present["subtree"]) {
			return Provenance{}, fmt.Errorf("%s %s record names no signed object", shape.article, rec.Type)
		}
		if shape.namesKey && p.present["identity"] {
			return Provenance{}, fmt.Errorf("%s %s record names a key, not an identity", shape.article, rec.Type)
		}
		if !shape.namesKey && p.present["key"] {
			return Provenance{}, fmt.Errorf("%s %s record names an identity, not a key", shape.article, rec.Type)
		}
		if !shape.namesRepo && p.present["repo"] {
			return Provenance{}, fmt.Errorf("%s %s record names no repository", shape.article, rec.Type)
		}
	}
	if r.ObjectFormat != nil {
		rec.ObjectFormat = string(*r.ObjectFormat)
	}
	if r.Object != nil {
		rec.Object = string(*r.Object)
	}
	if p.present["subtree"] {
		if r.Subtree == nil || *r.Subtree == "" {
			return Provenance{}, errors.New("a subtree written is never empty: the root writes none")
		}
		rec.Subtree = string(*r.Subtree)
	}
	if p.present["repo"] {
		if r.Repo == nil || *r.Repo == "" {
			return Provenance{}, errors.New("a repo written is never empty")
		}
		rec.Repo = string(*r.Repo)
	}
	if r.Identity != nil {
		rec.SAN, rec.Issuer = string(r.Identity.SAN), string(r.Identity.Issuer)
	}
	if r.Key != nil {
		rec.KeyKind, rec.KeyFingerprint = string(r.Key.Kind), string(r.Key.Fingerprint)
	}
	return rec, nil
}

// Version and Ref are free-string facts and decode via rawScalar so no
// spelling takes goccy's typed-coercion path (0x1f -> "31"); Path,
// Digest, and Modfile stay plain strings because their grammars admit no
// coercible spelling — any coerced output fails their validation.
type rawModule struct {
	Path       string    `yaml:"path"`
	Version    rawScalar `yaml:"version"`
	Digest     string    `yaml:"digest"`
	Modfile    string    `yaml:"modfile"`
	Provenance provNode  `yaml:"provenance"`
}

type rawPlugin struct {
	Ref        rawScalar `yaml:"ref"`
	Scheme     string    `yaml:"scheme"`
	Digest     string    `yaml:"digest"`
	Provenance provNode  `yaml:"provenance"`
}

// pluginEntryKeys are the keys each scheme's entries may carry — "the
// scheme's own facts and no others" (REQ-lock-plugin-entry). The claim
// is about KEYS: an empty or null value decodes to a zero struct field
// indistinguishable from absence, and goccy skips custom unmarshalers
// for null entirely, so presence is read off the AST, not the value.
var pluginEntryKeys = map[string]map[string]bool{
	SchemeOCI: {"ref": true, "scheme": true, "digest": true, "provenance": true},
}

// refuseUnrecordedSchemes walks the plugins sequence of the document
// for an entry written with a scheme the lockfile does not record and
// refuses the first naming the scheme, whatever its other keys
// (REQ-lock-plugin-entry): a local plugin's, from a lockfile written
// when it did, naming the fix; any other as unknown. The walk reads
// the document, not the decode, so the keys the scheme had never
// speak first; an entry writing no scheme is the decode's as before.
func refuseUnrecordedSchemes(mapping *ast.MappingNode) error {
	for _, kv := range mapping.Values {
		if s, ok := kv.Key.(*ast.StringNode); !ok || s.Value != "plugins" {
			continue
		}
		seq, ok := kv.Value.(*ast.SequenceNode)
		if !ok {
			return nil
		}
		for i, entry := range seq.Values {
			em, ok := entry.(*ast.MappingNode)
			if !ok {
				continue
			}
			scheme, written := scalarOf(em, "scheme")
			if !written {
				continue
			}
			ref, _ := scalarOf(em, "ref")
			name := fmt.Sprintf("plugin %q", ref)
			if ref == "" {
				name = fmt.Sprintf("plugin entry %d", i+1)
			}
			switch scheme {
			case SchemeOCI:
			case plugin.SchemeLocal:
				return fmt.Errorf("%w: %s: a local plugin has no lockfile entry; remove it", ErrInvalid, name)
			default:
				return fmt.Errorf("%w: %s: unknown scheme %q", ErrInvalid, name, scheme)
			}
		}
	}
	return nil
}

// nodeOf is the value node of a mapping's key, nil where the key is
// not written.
func nodeOf(m *ast.MappingNode, key string) ast.Node {
	for _, kv := range m.Values {
		if k, ok := kv.Key.(*ast.StringNode); ok && k.Value == key {
			return kv.Value
		}
	}
	return nil
}

// scalarOf is the scalar value of a mapping's key as the document
// spells it — plain, quoted or a block scalar, an integer as its
// digits — and whether the key holds one.
func scalarOf(m *ast.MappingNode, key string) (string, bool) {
	switch v := nodeOf(m, key).(type) {
	case *ast.StringNode:
		return v.Value, true
	case *ast.LiteralNode:
		return v.Value.Value, true
	case *ast.IntegerNode:
		return fmt.Sprint(v.Value), true
	}
	return "", false
}

// pluginKeySets walks the plugins sequence of the document mapping and
// returns each entry's key names, index-aligned with the decoded
// entries. Non-mapping shapes yield nil; the strict decode has its own
// error for them.
func pluginKeySets(mapping *ast.MappingNode) [][]string {
	var sets [][]string
	for _, kv := range mapping.Values {
		if s, ok := kv.Key.(*ast.StringNode); !ok || s.Value != "plugins" {
			continue
		}
		seq, ok := kv.Value.(*ast.SequenceNode)
		if !ok {
			return nil
		}
		for _, entry := range seq.Values {
			em, ok := entry.(*ast.MappingNode)
			if !ok {
				sets = append(sets, nil)
				continue
			}
			var keys []string
			for _, ekv := range em.Values {
				if s, ok := ekv.Key.(*ast.StringNode); ok {
					keys = append(keys, s.Value)
				}
			}
			sets = append(sets, keys)
		}
	}
	return sets
}

type rawFile struct {
	Version  int         `yaml:"version"`
	Modules  []rawModule `yaml:"modules"`
	Rulesets []rawModule `yaml:"rulesets"`
	Plugins  []rawPlugin `yaml:"plugins"`
}

// Parse decodes and validates lockfile bytes (REQ-lock-format,
// REQ-lock-entry, REQ-lock-ruleset-entry): exactly one YAML document,
// top-level keys version (the integer 1), modules, and — only when
// such pins exist — rulesets and plugins; no merge keys anywhere.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing version key", ErrInvalid)
	}
	var raw rawFile
	// The version first (REQ-lock-format): a document of another
	// version, written as the integer the clause names, is refused as
	// such before anything in it is judged; another spelling is the
	// decode's to refuse as before.
	if n, ok := nodeOf(mapping, "version").(*ast.IntegerNode); ok && fmt.Sprint(n.Value) != "1" {
		return nil, fmt.Errorf("%w: unsupported lockfile version %v", ErrInvalid, n.Value)
	}
	// A plugin entry of a scheme the lockfile does not record is
	// refused naming the scheme, whatever its other keys: read off the
	// document before the strict decode, which would otherwise refuse
	// the keys the scheme had (REQ-lock-plugin-entry).
	if err := refuseUnrecordedSchemes(mapping); err != nil {
		return nil, err
	}
	if err := yaml.NodeToValue(mapping, &raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("%w: unsupported lockfile version %d", ErrInvalid, raw.Version)
	}
	f := &File{}
	for _, l := range []struct {
		what string
		raw  []rawModule
		into *[]ModulePin
	}{{"module", raw.Modules, &f.Modules}, {"ruleset", raw.Rulesets, &f.Rulesets}} {
		for _, m := range l.raw {
			prov, err := m.Provenance.record()
			if err != nil {
				return nil, fmt.Errorf("%w: %s %q: %v", ErrInvalid, l.what, m.Path, err)
			}
			*l.into = append(*l.into, ModulePin{
				Path: m.Path, Version: string(m.Version), Digest: m.Digest, Modfile: m.Modfile, Provenance: prov,
			})
		}
	}
	pluginKeys := pluginKeySets(mapping)
	for i, p := range raw.Plugins {
		pin := PluginPin{Ref: string(p.Ref), Scheme: p.Scheme, Digest: p.Digest}
		// Scheme/key agreement is checked against the entry's AST keys
		// (see pluginEntryKeys); the value shapes are validate's job.
		if allowed, known := pluginEntryKeys[p.Scheme]; known && i < len(pluginKeys) {
			for _, key := range pluginKeys[i] {
				if !allowed[key] {
					return nil, fmt.Errorf("%w: plugin %q: %s pins carry no %s key", ErrInvalid, p.Ref, p.Scheme, key)
				}
			}
		}
		// Every scheme the decode admits records a provenance; a scheme
		// the lockfile does not record was refused before the decode.
		prov, err := p.Provenance.record()
		if err != nil {
			return nil, fmt.Errorf("%w: plugin %q: %v", ErrInvalid, p.Ref, err)
		}
		pin.Provenance = prov
		f.Plugins = append(f.Plugins, pin)
	}
	if err := validate(f); err != nil {
		return nil, err
	}
	return f, nil
}

// Module returns the modules' pin for (path, version).
func (f *File) Module(path, version string) (ModulePin, bool) {
	return f.ModulePins().Module(path, version)
}

// Plugin returns the pin for (ref, scheme): a pin satisfies only lookups
// in its own scheme, so an entry migrated between schemes takes a fresh
// first-use pin.
func (f *File) Plugin(ref, scheme string) (PluginPin, bool) {
	if i := f.pluginIndex(ref, scheme); i >= 0 {
		return f.Plugins[i], true
	}
	return PluginPin{}, false
}

// AddPlugin records a plugin's first-use pin (REQ-lock-first-use,
// REQ-lock-plugin-entry): it is an error if a pin for (ref, scheme)
// already exists — pins are only added or explicitly updated, never
// silently rewritten.
func (f *File) AddPlugin(pin PluginPin) error {
	if err := checkPluginPin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if f.pluginIndex(pin.Ref, pin.Scheme) >= 0 {
		return fmt.Errorf("%w: plugin pin for %s (%s) already exists", ErrPinMismatch, pin.Ref, pin.Scheme)
	}
	f.Plugins = append(f.Plugins, pin)
	return nil
}

// pluginIndex is the position of the pin for (ref, scheme), -1 for
// none.
func (f *File) pluginIndex(ref, scheme string) int {
	for i, p := range f.Plugins {
		if p.Ref == ref && p.Scheme == scheme {
			return i
		}
	}
	return -1
}

// AddModule records a first-use module pin (REQ-lock-first-use), as
// Pins.Add does.
func (f *File) AddModule(pin ModulePin) error { return f.ModulePins().Add(pin) }

// UpdateModule is the modules' explicit update path
// (REQ-lock-no-silent-downgrade), as Pins.Update is.
func (f *File) UpdateModule(pin ModulePin) error { return f.ModulePins().Update(pin) }

// UpdatePlugin replaces the oci pin for pin.Ref with pin: the explicit
// user-invoked update REQ-lock-no-silent-downgrade sanctions, so no
// transition is judged. A reference with no oci pin is a mismatch.
func (f *File) UpdatePlugin(pin PluginPin) error {
	if err := checkPluginPin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if pin.Scheme != SchemeOCI {
		return fmt.Errorf("%w: plugin %q: only an oci pin is updated", ErrInvalid, pin.Ref)
	}
	i := f.pluginIndex(pin.Ref, SchemeOCI)
	if i < 0 {
		return fmt.Errorf("%w: no oci pin for %s to update", ErrPinMismatch, pin.Ref)
	}
	f.Plugins[i] = pin
	return nil
}

// VerifyModule enforces a fetched artifact against its module pin
// (REQ-lock-digest-enforcement). A pinned digest must equal computedDigest
// — including when computedDigest is empty; the digest spans the module's
// whole file set, so a stripped or altered in-archive module file always
// surfaces here. A pinned module-file hash is enforced only when
// computedModfile is non-empty: an empty computedModfile means the caller
// did not compute one this call (a synthesized module file lives outside
// the archive), not that the artifact was verified without it. A mismatch
// names the module, version, expected, and computed values; no pin is
// updated as a side effect. An empty pinned digest (archive never fetched)
// is NOT enforced: recording the first-use digest is the caller's separate
// responsibility via AddModule/UpdateModule — silent success here is
// absence of a pin, not verification.
func (f *File) VerifyModule(path, version, computedDigest, computedModfile string) error {
	return f.ModulePins().Verify(path, version, computedDigest, computedModfile)
}

// CheckProvenanceTransition guards non-explicit re-resolution
// (REQ-lock-no-silent-downgrade): a pin whose record names verified
// evidence must not transition to none, a different type, or a different
// identity outside UpdateModule.
func CheckProvenanceTransition(old, new Provenance) error {
	if old == (Provenance{}) {
		return nil
	}
	if new == (Provenance{}) {
		return fmt.Errorf("%w: verified record would become none", ErrProvenanceDowngrade)
	}
	if old.Type != new.Type {
		return fmt.Errorf("%w: evidence type %q would become %q", ErrProvenanceDowngrade, old.Type, new.Type)
	}
	if old.SAN != new.SAN || old.Issuer != new.Issuer {
		return fmt.Errorf("%w: identity %q/%q would become %q/%q", ErrProvenanceDowngrade, old.SAN, old.Issuer, new.SAN, new.Issuer)
	}
	if old.KeyKind != new.KeyKind || old.KeyFingerprint != new.KeyFingerprint {
		// A pinned key is the record's identity: another key is another
		// signer.
		return fmt.Errorf("%w: pinned key %s %s would become %s %s", ErrProvenanceDowngrade, old.KeyKind, old.KeyFingerprint, new.KeyKind, new.KeyFingerprint)
	}
	if old.Object != new.Object || old.ObjectFormat != new.ObjectFormat {
		// A different signed object for the same (path, version) means the
		// origin tag moved: a rewrite, never a silent refresh.
		return fmt.Errorf("%w: signed object %s (%s) would become %s (%s)", ErrProvenanceDowngrade, old.Object, old.ObjectFormat, new.Object, new.ObjectFormat)
	}
	return nil
}

// CheckModfileConsistency enforces REQ-lock-modfile-consistency: when both
// a standalone module file and the module archive have been fetched for the
// same pinned version, the standalone bytes must hash to the pinned
// module-file hash and equal the archive's module file bytes.
func CheckModfileConsistency(pinModfile string, standalone, archiveCopy []byte) error {
	sum := sha256.Sum256(standalone)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if pinModfile != "" && got != pinModfile {
		return fmt.Errorf("%w: standalone module file hashes to %s, pin says %s", ErrPinMismatch, got, pinModfile)
	}
	if archiveCopy != nil && !bytes.Equal(standalone, archiveCopy) {
		return fmt.Errorf("%w: standalone module file differs from the archive's copy", ErrPinMismatch)
	}
	return nil
}
