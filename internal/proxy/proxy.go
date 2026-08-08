// Package proxy implements the client side of the module proxy
// protocol's wire shapes (module-proxy.md): path/version escaping for
// case-insensitive storage, endpoint URL construction under a proxy base
// URL (REQ-proxy-endpoints), and parsing of the list, info, and
// provenance-envelope responses (REQ-proxy-prov-envelope,
// REQ-proxy-prov-unknown). Everything here is pure — fetching, source
// lists, and fall-through policy live above these shapes.
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/greatliontech/pb/internal/version"
)

// ErrMalformed is wrapped by every rejection of a response body: a
// malformed response aborts the fetch (REQ-proxy-fallthrough), so these
// errors are never "not here".
var ErrMalformed = errors.New("malformed proxy response")

// Escape encodes a module path or version string for use in a proxy URL
// (the escaped-path term): every uppercase ASCII letter becomes '!'
// followed by its lowercase form, and '!' itself becomes "!!", leaving
// the result safe for case-insensitive storage.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte('!')
			b.WriteByte(c - 'A' + 'a')
		case c == '!':
			b.WriteString("!!")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Unescape inverts Escape: '!' followed by a lowercase ASCII letter
// decodes to the uppercase letter, "!!" decodes to '!', and any other
// use of '!' — including a trailing one — is malformed. An uppercase
// ASCII letter in the input is likewise malformed: escaped strings are
// case-folded by construction, so its presence means the input never
// came from Escape.
func Unescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			return "", fmt.Errorf("uppercase %q in escaped string %q", c, s)
		case c != '!':
			b.WriteByte(c)
		case i+1 >= len(s):
			return "", fmt.Errorf("trailing '!' in escaped string %q", s)
		default:
			i++
			next := s[i]
			switch {
			case next == '!':
				b.WriteByte('!')
			case next >= 'a' && next <= 'z':
				b.WriteByte(next - 'a' + 'A')
			default:
				return "", fmt.Errorf("invalid escape %q in escaped string %q", s[i-1:i+1], s)
			}
		}
	}
	return b.String(), nil
}

// join builds an endpoint URL: the base with a trailing slash trimmed,
// the escaped module path, and the endpoint suffix. base must be an
// absolute URL without query or fragment, carrying at most one trailing
// slash — the source-list configuration validates entries before they
// reach these constructors.
func join(base, modulePath, suffix string) string {
	return strings.TrimSuffix(base, "/") + "/" + Escape(modulePath) + "/" + suffix
}

// versionURL builds a version-addressed artifact URL: @v/<escaped
// version><ext> under the module's base.
func versionURL(base, modulePath string, v version.Version, ext string) string {
	return join(base, modulePath, "@v/"+Escape(v.String())+ext)
}

// ListURL returns the @v/list endpoint for a module under a proxy base.
func ListURL(base, modulePath string) string {
	return join(base, modulePath, "@v/list")
}

// LatestURL returns the @latest endpoint for a module under a proxy base.
func LatestURL(base, modulePath string) string {
	return join(base, modulePath, "@latest")
}

// InfoURL returns the version's .info endpoint.
func InfoURL(base, modulePath string, v version.Version) string {
	return versionURL(base, modulePath, v, ".info")
}

// ModURL returns the version's .mod endpoint.
func ModURL(base, modulePath string, v version.Version) string {
	return versionURL(base, modulePath, v, ".mod")
}

// ZipURL returns the version's .zip endpoint.
func ZipURL(base, modulePath string, v version.Version) string {
	return versionURL(base, modulePath, v, ".zip")
}

// ProvURL returns the version's .prov endpoint.
func ProvURL(base, modulePath string, v version.Version) string {
	return versionURL(base, modulePath, v, ".prov")
}

// ParseList parses an @v/list body: zero or more tagged release
// versions, one per line (REQ-proxy-endpoints). A line that is not a
// canonical tagged release — pseudo-versions included, they are never
// tagged releases — is malformed. The list is advisory
// (REQ-proxy-list-advisory): callers use it for discovery only, never
// for the correctness of resolving declared versions.
func ParseList(body []byte) ([]version.Version, error) {
	var out []version.Version
	for line := range strings.Lines(string(body)) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		v, err := version.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("%w: list line %q: %v", ErrMalformed, line, err)
		}
		if v.IsPseudo() {
			return nil, fmt.Errorf("%w: list line %q is a pseudo-version, not a tagged release", ErrMalformed, line)
		}
		out = append(out, v)
	}
	return out, nil
}

// Info is a version's info object: the canonical version and, when the
// proxy served one, the version's timestamp.
type Info struct {
	Version version.Version
	Time    time.Time // zero when the response carried none
}

// ParseInfo parses an .info or @latest body: a JSON object with
// "version" (canonical version string) and optionally "time" (RFC 3339)
// per REQ-proxy-endpoints. Unknown members are ignored — the spec pins
// the two members it names, and rejecting additions would turn every
// proxy extension into a breaking change.
func ParseInfo(body []byte) (Info, error) {
	var raw struct {
		Version string  `json:"version"`
		Time    *string `json:"time"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Info{}, fmt.Errorf("%w: info object: %v", ErrMalformed, err)
	}
	v, err := version.Parse(raw.Version)
	if err != nil {
		return Info{}, fmt.Errorf("%w: info version %q: %v", ErrMalformed, raw.Version, err)
	}
	info := Info{Version: v}
	// A present time must be RFC 3339 — the empty string is not absence,
	// it is malformed. A JSON null decodes to a nil pointer and reads as
	// absent, Go's conventional treatment of optional members.
	if raw.Time != nil {
		t, err := time.Parse(time.RFC3339, *raw.Time)
		if err != nil {
			return Info{}, fmt.Errorf("%w: info time %q: %v", ErrMalformed, *raw.Time, err)
		}
		info.Time = t
	}
	return info, nil
}

// GitSignedTag is a decoded git-signed-tag evidence object
// (REQ-proxy-prov-envelope): the raw signed tag and commit objects and
// the tree objects from the commit's root tree toward the module root
// (empty for a module at the repository root).
type GitSignedTag struct {
	ObjectFormat string
	Tag          []byte
	Commit       []byte
	TreePath     [][]byte
}

// Envelope is a parsed provenance envelope: the recognized evidence,
// with unrecognized evidence types ignored (REQ-proxy-prov-unknown).
type Envelope struct {
	GitSignedTags []GitSignedTag
}

// envelopeFormatVersion is the one provenance envelope format this
// consumer supports; an envelope declaring any other fails
// (REQ-proxy-prov-unknown).
const envelopeFormatVersion = 1

// ParseEnvelope parses a .prov body. An unsupported formatVersion fails;
// evidence objects of unrecognized type are ignored; a recognized
// git-signed-tag object missing a required field or carrying invalid
// base64 or an unknown objectFormat is malformed.
func ParseEnvelope(body []byte) (Envelope, error) {
	var raw struct {
		FormatVersion *int               `json:"formatVersion"`
		Evidence      *[]json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Envelope{}, fmt.Errorf("%w: provenance envelope: %v", ErrMalformed, err)
	}
	if raw.FormatVersion == nil {
		return Envelope{}, fmt.Errorf("%w: provenance envelope missing formatVersion", ErrMalformed)
	}
	if *raw.FormatVersion != envelopeFormatVersion {
		return Envelope{}, fmt.Errorf("%w: unsupported provenance formatVersion %d (supported: %d)",
			ErrMalformed, *raw.FormatVersion, envelopeFormatVersion)
	}
	// The term defines the envelope as formatVersion AND evidence, a
	// list; a document lacking the member is not an envelope with no
	// evidence, it is not an envelope.
	if raw.Evidence == nil {
		return Envelope{}, fmt.Errorf("%w: provenance envelope missing evidence", ErrMalformed)
	}
	var env Envelope
	for i, entry := range *raw.Evidence {
		// Two-phase decode: only the entry boundary — a JSON object
		// carrying a string type — is common to all evidence types (the
		// envelope term). An unrecognized type's remaining fields stay
		// opaque (REQ-proxy-prov-unknown): decoding them against a
		// recognized shape would let a future type that reuses a field
		// name at a different JSON type poison the whole envelope.
		var head struct {
			Type *string `json:"type"`
		}
		if err := json.Unmarshal(entry, &head); err != nil {
			return Envelope{}, fmt.Errorf("%w: evidence %d is not an object: %v", ErrMalformed, i, err)
		}
		if head.Type == nil {
			return Envelope{}, fmt.Errorf("%w: evidence %d carries no type", ErrMalformed, i)
		}
		if *head.Type != "git-signed-tag" {
			continue
		}
		var ev struct {
			ObjectFormat string   `json:"objectFormat"`
			Tag          *string  `json:"tag"`
			Commit       *string  `json:"commit"`
			TreePath     []string `json:"treePath"`
		}
		if err := json.Unmarshal(entry, &ev); err != nil {
			return Envelope{}, fmt.Errorf("%w: evidence %d: %v", ErrMalformed, i, err)
		}
		if ev.ObjectFormat != "sha1" && ev.ObjectFormat != "sha256" {
			return Envelope{}, fmt.Errorf("%w: evidence %d: objectFormat %q is neither sha1 nor sha256", ErrMalformed, i, ev.ObjectFormat)
		}
		if ev.Tag == nil || ev.Commit == nil || ev.TreePath == nil {
			return Envelope{}, fmt.Errorf("%w: evidence %d: git-signed-tag object missing a required field", ErrMalformed, i)
		}
		// Canonical base64 only (REQ-proxy-prov-envelope): no embedded
		// whitespace, no nonzero spare trailing bits — two wire spellings
		// of the same bytes would undermine byte-identity reasoning. A
		// raw git object always carries at least its header, so an empty
		// decode is malformed, not an empty object.
		decode := func(field, s string) ([]byte, error) {
			if strings.ContainsAny(s, " \t\r\n") {
				return nil, fmt.Errorf("%w: evidence %d: %s carries whitespace inside base64", ErrMalformed, i, field)
			}
			b, err := base64.StdEncoding.Strict().DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("%w: evidence %d: %s is not canonical base64: %v", ErrMalformed, i, field, err)
			}
			if len(b) == 0 {
				return nil, fmt.Errorf("%w: evidence %d: %s decodes to an empty object", ErrMalformed, i, field)
			}
			return b, nil
		}
		tag, err := decode("tag", *ev.Tag)
		if err != nil {
			return Envelope{}, err
		}
		commit, err := decode("commit", *ev.Commit)
		if err != nil {
			return Envelope{}, err
		}
		trees := make([][]byte, 0, len(ev.TreePath))
		for _, t := range ev.TreePath {
			b, err := decode("treePath entry", t)
			if err != nil {
				return Envelope{}, err
			}
			trees = append(trees, b)
		}
		env.GitSignedTags = append(env.GitSignedTags, GitSignedTag{
			ObjectFormat: ev.ObjectFormat,
			Tag:          tag,
			Commit:       commit,
			TreePath:     trees,
		})
	}
	return env, nil
}
