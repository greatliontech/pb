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
	"maps"
	"reflect"
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
)

// Provenance is a lockfile provenance record (REQ-lock-provenance-record).
// The zero value is the literal `none`.
type Provenance struct {
	Type         string // ProvenanceGitSignedTag or ProvenanceImageSignature
	ObjectFormat string // git-signed-tag: "sha1" or "sha256"
	Object       string // git-signed-tag: hex git hash of the signed object
	SAN          string
	Issuer       string
}

// ModulePin is one module entry (REQ-lock-entry).
type ModulePin struct {
	Path       string
	Version    string
	Digest     string // "pb1:" + 64 hex; empty when the archive has not been fetched
	Modfile    string // "sha256:" + 64 hex; empty when the module declares no module file
	Provenance Provenance
}

// Plugin identity schemes (REQ-lock-plugin-entry), named from their one
// home for callers already reaching them through this package.
const (
	SchemeOCI   = plugin.SchemeOCI
	SchemeLocal = plugin.SchemeLocal
)

// PluginPin is one plugin entry (REQ-lock-plugin-entry): identity facts
// for the (ref, scheme) pair. An oci pin carries Digest and Provenance;
// a local pin carries Binary — platform-keyed content hashes — and no
// provenance at all: a host binary has no evidence to record, and its
// absence is not spelled `none`.
type PluginPin struct {
	Ref        string // plugin identity as written in generation configuration, without a digest
	Scheme     string // SchemeOCI or SchemeLocal — stated, never inferred from fields
	Digest     string // oci: "sha256:" + 64 hex manifest-list digest
	Provenance Provenance
	Binary     map[string]string // local: "<os>/<arch>" -> "sha256:" + 64 hex
}

// File is a parsed lockfile: pins only.
type File struct {
	Modules []ModulePin
	Plugins []PluginPin
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
		// A ref is a local plugin's path as written where it names
		// one, so it may start with "/" or "./" — neither a YAML
		// indicator, both re-parsing as themselves; "." alone leads
		// the float spellings (.inf, .nan) and stays out. No other
		// fact has a reason to start so.
		pathStart := kind == "ref" && (c == '/' || strings.HasPrefix(s, "./"))
		bad = !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || pathStart)
	}
	for i := 0; !bad && i < len(s); i++ {
		bad = s[i] < '!' || s[i] > '~'
	}
	if bad {
		starts := "alphanumeric"
		if kind == "ref" {
			starts = `alphanumeric, with "/" or with "./"`
		}
		return fmt.Errorf("%s %q is not plain-scalar safe: values start %s, use printable non-space ASCII, are not a null spelling, and do not end with %q", kind, s, starts, ":")
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

// checkProvenance validates a record against the evidence type its
// entry kind admits (REQ-lock-provenance-record).
func checkProvenance(p Provenance, admitted string) error {
	if p == (Provenance{}) {
		return nil
	}
	if p.Type != ProvenanceGitSignedTag && p.Type != ProvenanceImageSignature {
		return fmt.Errorf("unknown provenance type %q", p.Type)
	}
	if p.Type != admitted {
		return fmt.Errorf("provenance type %q is not this entry's (%s)", p.Type, admitted)
	}
	switch p.Type {
	case ProvenanceGitSignedTag:
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
	case ProvenanceImageSignature:
		if p.ObjectFormat != "" || p.Object != "" {
			return fmt.Errorf("an %s record names no signed object", ProvenanceImageSignature)
		}
	}
	if p.SAN == "" || p.Issuer == "" {
		return errors.New("identity needs both san and issuer")
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
	if err := checkProvenance(m.Provenance, ProvenanceGitSignedTag); err != nil {
		return fmt.Errorf("module %q: %v", m.Path, err)
	}
	return nil
}

// checkPlatform bounds a Binary key to "<os>/<arch>": two non-empty
// lowercase-alphanumeric segments.
func checkPlatform(s string) error {
	osPart, arch, ok := strings.Cut(s, "/")
	bad := !ok || osPart == "" || arch == ""
	for _, part := range []string{osPart, arch} {
		for i := 0; !bad && i < len(part); i++ {
			c := part[i]
			bad = !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z')
		}
	}
	if bad {
		return fmt.Errorf("platform %q is not <os>/<arch> in lowercase alphanumerics", s)
	}
	return nil
}

func checkPluginPin(p PluginPin) error {
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
		if p.Binary != nil {
			return fmt.Errorf("plugin %q: oci pins carry no binary hashes", p.Ref)
		}
		if err := checkHashRef("sha256:", p.Digest); err != nil {
			return fmt.Errorf("plugin %q: %v", p.Ref, err)
		}
		if err := checkProvenance(p.Provenance, ProvenanceImageSignature); err != nil {
			return fmt.Errorf("plugin %q: %v", p.Ref, err)
		}
	case SchemeLocal:
		if p.Digest != "" {
			return fmt.Errorf("plugin %q: local pins carry no digest", p.Ref)
		}
		if p.Provenance != (Provenance{}) {
			return fmt.Errorf("plugin %q: local pins carry no provenance", p.Ref)
		}
		if len(p.Binary) == 0 {
			return fmt.Errorf("plugin %q: local pin has no binary hashes", p.Ref)
		}
		for platform, hash := range p.Binary {
			if err := checkPlatform(platform); err != nil {
				return fmt.Errorf("plugin %q: %v", p.Ref, err)
			}
			if err := checkHashRef("sha256:", hash); err != nil {
				return fmt.Errorf("plugin %q, platform %s: %v", p.Ref, platform, err)
			}
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
	seenM := make(map[[2]string]struct{}, len(f.Modules))
	for _, m := range f.Modules {
		if err := checkModulePin(m); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		k := [2]string{m.Path, m.Version}
		if _, dup := seenM[k]; dup {
			return fmt.Errorf("%w: duplicate module pin %s@%s", ErrInvalid, m.Path, m.Version)
		}
		seenM[k] = struct{}{}
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
	slices.SortFunc(f.Modules, func(a, b ModulePin) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	slices.SortFunc(f.Plugins, func(a, b PluginPin) int {
		if c := strings.Compare(a.Ref, b.Ref); c != 0 {
			return c
		}
		return strings.Compare(a.Scheme, b.Scheme)
	})
}

// Encode renders the lockfile canonically (REQ-lock-canonical-emission,
// REQ-lock-format): version 1, modules sorted by (path, version), plugins
// sorted by ref, fixed key order, two-space indent, block style, LF,
// every value a plain scalar — the domain REQ-lock-scalar-values bounds
// is what the reader takes raw, so no value is quoted — and the
// rendering held to its reading. Emission is a pure function of the
// recorded facts.
func Encode(f *File) ([]byte, error) {
	if err := validate(f); err != nil {
		return nil, err
	}
	c := &File{Modules: slices.Clone(f.Modules), Plugins: slices.Clone(f.Plugins)}
	sortPins(c)
	return contractfile.Emit(func(w *contractfile.Writer) {
		w.Literal("version", "1")
		w.Sequence("modules", len(c.Modules), func(i int) {
			m := c.Modules[i]
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
		if len(c.Plugins) > 0 {
			w.Sequence("plugins", len(c.Plugins), func(i int) {
				p := c.Plugins[i]
				w.Literal("ref", p.Ref)
				w.Literal("scheme", p.Scheme)
				switch p.Scheme {
				case SchemeOCI:
					w.Literal("digest", p.Digest)
					writeProvenance(w, p.Provenance)
				case SchemeLocal:
					w.Mapping("binary", func() {
						for _, platform := range slices.Sorted(maps.Keys(p.Binary)) {
							w.Literal(platform, p.Binary[platform])
						}
					})
				}
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
// empty one alike — a file may pin no module and no plugin.
func pinsOf(f *File) File {
	var out File
	if len(f.Modules) > 0 {
		out.Modules = f.Modules
	}
	if len(f.Plugins) > 0 {
		out.Plugins = f.Plugins
	}
	return out
}

// writeProvenance writes a pin's provenance record: `none` where it
// has none, else its type and, for a signed tag, the object, then the
// identity.
func writeProvenance(w *contractfile.Writer, p Provenance) {
	if p == (Provenance{}) {
		w.Literal("provenance", "none")
		return
	}
	w.Mapping("provenance", func() {
		w.Literal("type", p.Type)
		if p.Type == ProvenanceGitSignedTag {
			w.Literal("objectFormat", p.ObjectFormat)
			w.Literal("object", p.Object)
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

type rawProvenance struct {
	Type         rawScalar   `yaml:"type"`
	ObjectFormat rawScalar   `yaml:"objectFormat"`
	Object       rawScalar   `yaml:"object"`
	Identity     rawIdentity `yaml:"identity"`
}

// provNode captures the provenance value's raw YAML fragment
// (BytesUnmarshaler), so scalars keep their exact spelling — no
// map[string]any type-coercion detour: a digits-only object hash stays a
// string, and identity values are preserved byte-faithfully.
type provNode struct {
	set bool
	rec rawProvenance
}

func (p *provNode) UnmarshalYAML(b []byte) error {
	p.set = true
	if strings.TrimSpace(string(b)) == "none" {
		// The zero record is the none record; no separate flag needed.
		return nil
	}
	if err := yaml.UnmarshalWithOptions(b, &p.rec, yaml.Strict()); err != nil {
		return fmt.Errorf("provenance is neither none nor a record: %v", err)
	}
	if p.rec == (rawProvenance{}) {
		// The zero record must never masquerade as the spelled none: an
		// empty or degenerate record mapping is a mangled lockfile, and
		// reading it as unsigned would erase provenance silently.
		return errors.New("provenance record is empty")
	}
	return nil
}

func (p *provNode) record() (Provenance, error) {
	if !p.set {
		return Provenance{}, errors.New("missing provenance")
	}
	return Provenance{
		Type: string(p.rec.Type), ObjectFormat: string(p.rec.ObjectFormat), Object: string(p.rec.Object),
		SAN: string(p.rec.Identity.SAN), Issuer: string(p.rec.Identity.Issuer),
	}, nil
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
	Ref        rawScalar            `yaml:"ref"`
	Scheme     string               `yaml:"scheme"`
	Digest     string               `yaml:"digest"`
	Provenance provNode             `yaml:"provenance"`
	Binary     map[string]rawScalar `yaml:"binary"`
}

// pluginEntryKeys are the keys each scheme's entries may carry — "the
// scheme's own facts and no others" (REQ-lock-plugin-entry). The claim
// is about KEYS: an empty or null value decodes to a zero struct field
// indistinguishable from absence, and goccy skips custom unmarshalers
// for null entirely, so presence is read off the AST, not the value.
var pluginEntryKeys = map[string]map[string]bool{
	SchemeOCI:   {"ref": true, "scheme": true, "digest": true, "provenance": true},
	SchemeLocal: {"ref": true, "scheme": true, "binary": true},
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
	Version int         `yaml:"version"`
	Modules []rawModule `yaml:"modules"`
	Plugins []rawPlugin `yaml:"plugins"`
}

// Parse decodes and validates lockfile bytes (REQ-lock-format,
// REQ-lock-entry): exactly one YAML document, top-level keys version (the
// integer 1), modules, and — only when plugin pins exist — plugins; no
// merge keys anywhere.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing version key", ErrInvalid)
	}
	var raw rawFile
	if err := yaml.NodeToValue(mapping, &raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("%w: unsupported lockfile version %d", ErrInvalid, raw.Version)
	}
	f := &File{}
	for _, m := range raw.Modules {
		prov, err := m.Provenance.record()
		if err != nil {
			return nil, fmt.Errorf("%w: module %q: %v", ErrInvalid, m.Path, err)
		}
		f.Modules = append(f.Modules, ModulePin{
			Path: m.Path, Version: string(m.Version), Digest: m.Digest, Modfile: m.Modfile, Provenance: prov,
		})
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
		if p.Scheme != SchemeLocal {
			// oci — and unknown schemes fail in validate with the better
			// error.
			prov, err := p.Provenance.record()
			if err != nil {
				return nil, fmt.Errorf("%w: plugin %q: %v", ErrInvalid, p.Ref, err)
			}
			pin.Provenance = prov
		}
		if p.Binary != nil {
			pin.Binary = make(map[string]string, len(p.Binary))
			for platform, hash := range p.Binary {
				pin.Binary[platform] = string(hash)
			}
		}
		f.Plugins = append(f.Plugins, pin)
	}
	if err := validate(f); err != nil {
		return nil, err
	}
	return f, nil
}

// Module returns the pin for (path, version).
func (f *File) Module(path, version string) (ModulePin, bool) {
	for _, m := range f.Modules {
		if m.Path == path && m.Version == version {
			return m, true
		}
	}
	return ModulePin{}, false
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

// SetPluginBinary records a local plugin's content hash for one host
// platform (REQ-plugin-local-pin, REQ-lock-plugin-entry): a first use
// on that platform adds the key to the existing local pin; a key
// already recorded must agree, a differing hash being a pin mismatch
// naming both. The pin itself must exist and be local — a first use
// on the first platform goes through AddPlugin.
func (f *File) SetPluginBinary(ref, platform, hash string) error {
	for i := range f.Plugins {
		p := &f.Plugins[i]
		if p.Ref != ref || p.Scheme != SchemeLocal {
			continue
		}
		if have, ok := p.Binary[platform]; ok {
			if have != hash {
				return fmt.Errorf("%w: plugin %s on %s is pinned to %s, resolved %s", ErrPinMismatch, ref, platform, have, hash)
			}
			return nil
		}
		next := PluginPin{Ref: p.Ref, Scheme: p.Scheme, Binary: maps.Clone(p.Binary)}
		next.Binary[platform] = hash
		if err := checkPluginPin(next); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		p.Binary = next.Binary
		return nil
	}
	return fmt.Errorf("%w: no local pin for plugin %s", ErrInvalid, ref)
}

// AddModule records a first-use pin (REQ-lock-first-use): it is an error if
// any pin for (path, version) already exists — pins are only added or
// explicitly updated, never silently rewritten.
func (f *File) AddModule(pin ModulePin) error {
	if err := checkModulePin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, exists := f.Module(pin.Path, pin.Version); exists {
		return fmt.Errorf("%w: pin for %s@%s already exists", ErrPinMismatch, pin.Path, pin.Version)
	}
	f.Modules = append(f.Modules, pin)
	return nil
}

// UpdateModule is the explicit update path (REQ-lock-no-silent-downgrade):
// the only operation that may change an existing pin, including weakening
// or altering its provenance record.
func (f *File) UpdateModule(pin ModulePin) error {
	if err := checkModulePin(pin); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for i, m := range f.Modules {
		if m.Path == pin.Path && m.Version == pin.Version {
			f.Modules[i] = pin
			return nil
		}
	}
	return fmt.Errorf("%w: no pin for %s@%s to update", ErrPinMismatch, pin.Path, pin.Version)
}

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

// VerifyModule enforces a fetched artifact against its pin
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
	pin, ok := f.Module(path, version)
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
