// Package version parses, validates, and orders pb module versions: tagged
// release versions `vX.Y.Z` with an optional prerelease suffix
// (REQ-modfile-versions) and pseudo-versions synthesized for untagged
// commits (the pseudo-version term in module-proxy.md). There is no build
// metadata: two versions compare equal only when their strings are equal,
// so the ordering is total — REQ-resolve-mvs's "maximum under
// semantic-version ordering" is well defined and REQ-resolve-determinism
// cannot be broken by an ordering tie.
//
// Pseudo-version ordering by embedded timestamp needs no special case: the
// timestamp is a fixed-width numeric prerelease identifier, so semantic-
// version precedence over the shared base already orders pseudo-versions
// chronologically.
package version

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid is wrapped by every version rejection.
var ErrInvalid = errors.New("invalid version")

// Version is a parsed, canonical version. The zero value is not valid;
// obtain one from Parse. Numeric components stay strings — canonical
// numbers carry no leading zeros, so length-then-byte comparison is exact
// numeric order with no magnitude bound and no integer overflow.
type Version struct {
	str        string
	major      string
	minor      string
	patch      string
	prerelease string // without the leading '-'; empty for a release
}

// String returns the canonical version string.
func (v Version) String() string { return v.str }

// Major returns the major version number's canonical digits.
func (v Version) Major() string { return v.major }

// CompareMajor orders two versions by major version number alone.
func CompareMajor(a, b Version) int { return cmpNum(a.major, b.major) }

// Parse validates and parses a version string: `v` MAJOR `.` MINOR `.`
// PATCH, each a non-negative integer without leading zeros, optionally
// followed by `-` and a dot-separated prerelease of alphanumeric-hyphen
// identifiers, numeric identifiers carrying no leading zeros. No build
// metadata exists in the version surface.
func Parse(s string) (Version, error) {
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return Version{}, fmt.Errorf("%w: %q does not start with v", ErrInvalid, s)
	}
	core, pre, hasPre := strings.Cut(rest, "-")
	nums := strings.Split(core, ".")
	if len(nums) != 3 {
		return Version{}, fmt.Errorf("%w: %q core is not MAJOR.MINOR.PATCH", ErrInvalid, s)
	}
	for _, n := range nums {
		if err := checkNum(n); err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalid, s, err)
		}
	}
	if hasPre {
		// An empty prerelease ("v1.2.3-") needs no dedicated guard:
		// splitting "" yields one empty identifier, which is rejected.
		for id := range strings.SplitSeq(pre, ".") {
			if err := checkPrereleaseID(id); err != nil {
				return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalid, s, err)
			}
		}
	}
	return Version{str: s, major: nums[0], minor: nums[1], patch: nums[2], prerelease: pre}, nil
}

// checkNum validates a canonical non-negative integer: digits only, no
// leading zeros, unbounded.
func checkNum(s string) error {
	if s == "" {
		return errors.New("empty number")
	}
	if len(s) > 1 && s[0] == '0' {
		return fmt.Errorf("number %q has a leading zero", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fmt.Errorf("number %q has a non-digit", s)
		}
	}
	return nil
}

// cmpNum orders canonical numeric digit strings: no leading zeros, so
// length then byte order is numeric order.
func cmpNum(a, b string) int {
	if c := cmpInt(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func checkPrereleaseID(id string) error {
	if id == "" {
		return errors.New("empty prerelease identifier")
	}
	numeric := true
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-':
			numeric = false
		default:
			return fmt.Errorf("prerelease identifier %q has an invalid character", id)
		}
	}
	if numeric && len(id) > 1 && id[0] == '0' {
		return fmt.Errorf("numeric prerelease identifier %q has a leading zero", id)
	}
	return nil
}

// Compare orders versions by semantic-version precedence: the numeric
// core, then release above any prerelease, then prerelease identifiers
// left to right (numeric below alphanumeric, numeric compared as numbers,
// alphanumeric in ASCII order, a shorter identifier list below a longer
// one it prefixes). With no build metadata, Compare(a, b) == 0 exactly
// when a.String() == b.String(), so the order is total.
func Compare(a, b Version) int {
	if c := cmpNum(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpNum(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpNum(a.patch, b.patch); c != 0 {
		return c
	}
	return cmpPrerelease(a.prerelease, b.prerelease)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpPrerelease(a, b string) int {
	if a == b {
		return 0
	}
	// A release outranks every prerelease of the same core.
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := cmpPrereleaseID(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpPrereleaseID(a, b string) int {
	an, bn := isNumericID(a), isNumericID(b)
	switch {
	case an && bn:
		return cmpNum(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumericID(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Max returns the larger of a and b under Compare.
func Max(a, b Version) Version {
	if Compare(a, b) >= 0 {
		return a
	}
	return b
}

// Pseudo reports whether v is a pseudo-version and, when it is, returns
// the embedded commit timestamp (UTC yyyymmddhhmmss) and 12-hex-digit
// commit hash prefix. The three shapes (module-proxy.md pseudo-version
// term): `v0.0.0-<ts>-<hash>` with no preceding release,
// `vX.Y.(Z+1)-0.<ts>-<hash>` after release `vX.Y.Z`, and
// `vX.Y.Z-<pre>.0.<ts>-<hash>` after prerelease `vX.Y.Z-<pre>`.
func (v Version) Pseudo() (timestamp, hash string, ok bool) {
	ids := strings.Split(v.prerelease, ".")
	last := ids[len(ids)-1]
	if len(last) != 27 || last[14] != '-' {
		return "", "", false
	}
	ts, h := last[:14], last[15:]
	if !isNumericID(ts) || !isLowerHex(h) {
		return "", "", false
	}
	switch {
	case len(ids) == 1:
		// v0.0.0-<ts>-<hash>: nothing precedes, base is the zero version.
		if v.major != "0" || v.minor != "0" || v.patch != "0" {
			return "", "", false
		}
	case ids[len(ids)-2] != "0":
		return "", "", false
	case len(ids) == 2 && v.patch == "0":
		// vX.Y.(Z+1)-0.<ts>-<hash> increments a release's patch, so a
		// patch of zero has no valid precedent and is not a pseudo shape.
		return "", "", false
	}
	return ts, h, true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsPseudo reports whether v is a pseudo-version.
func (v Version) IsPseudo() bool {
	_, _, ok := v.Pseudo()
	return ok
}

// PseudoVersion synthesizes the pseudo-version for an untagged commit:
// commitTime is rendered as UTC yyyymmddhhmmss and hash contributes its
// first 12 hex digits. precedent is the highest tagged release preceding
// the commit, or nil when none does; a pseudo-version is never a tagged
// release, so a pseudo precedent is refused rather than chained into a
// shape outside the term's three forms.
func PseudoVersion(precedent *Version, commitTime time.Time, hash string) (Version, error) {
	if len(hash) < 12 {
		return Version{}, fmt.Errorf("%w: commit hash %q is shorter than 12 digits", ErrInvalid, hash)
	}
	h := hash[:12]
	if !isLowerHex(h) {
		return Version{}, fmt.Errorf("%w: commit hash %q is not lowercase hex", ErrInvalid, hash)
	}
	if precedent != nil && precedent.IsPseudo() {
		return Version{}, fmt.Errorf("%w: precedent %s is a pseudo-version, not a tagged release", ErrInvalid, precedent)
	}
	suffix := commitTime.UTC().Format("20060102150405") + "-" + h
	var s string
	switch {
	case precedent == nil:
		s = "v0.0.0-" + suffix
	case precedent.prerelease != "":
		s = precedent.str + ".0." + suffix
	default:
		s = fmt.Sprintf("v%s.%s.%s-0.%s", precedent.major, precedent.minor, incNum(precedent.patch), suffix)
	}
	return Parse(s)
}

// incNum adds one to a canonical decimal digit string.
func incNum(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}
