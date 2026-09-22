package source

import (
	"fmt"
	"path"
	"strings"
)

// The module-path pattern grammar two settings share
// (module-proxy.md REQ-proxy-config, whose noproxy grammar
// REQ-resolve-ssh's ssh setting takes): path-glob patterns matched
// against the module path or any leading segment prefix of it, `*`
// never crossing a segment boundary.

// ParsePatterns parses a setting's value in the grammar: comma-separated
// glob patterns, each valid; an empty value is no pattern. what names
// the list in a refusal — "exclusion" for noproxy, "ssh" for ssh.
func ParsePatterns(list, what string) ([]string, error) {
	if list == "" {
		return nil, nil
	}
	var patterns []string
	for pat := range strings.SplitSeq(list, ",") {
		if pat == "" {
			return nil, fmt.Errorf("the %s list carries an empty pattern", what)
		}
		if err := CheckPattern(pat); err != nil {
			return nil, fmt.Errorf("%s pattern %q: %v", what, pat, err)
		}
		patterns = append(patterns, pat)
	}
	return patterns, nil
}

// CheckPattern reports whether the glob is valid. path.Match reports
// ErrBadPattern for a malformed pattern regardless of the name (Go
// 1.16+), so a single probe is a complete validity oracle and the
// grammar has exactly one owner; MatchPrefix may then discard Match
// errors — none can occur for a validated pattern.
func CheckPattern(pat string) error {
	_, err := path.Match(pat, "")
	return err
}

// MatchPrefix reports whether the glob matches the module path or any
// leading segment prefix of it; path.Match's '*' never crosses a '/',
// so matching stays segment-wise. Patterns are validated at parse, so
// a Match error cannot occur here and reads as a non-match.
func MatchPrefix(pattern, modulePath string) bool {
	prefix := modulePath
	for {
		if ok, _ := path.Match(pattern, prefix); ok {
			return true
		}
		i := strings.LastIndexByte(prefix, '/')
		if i < 0 {
			return false
		}
		prefix = prefix[:i]
	}
}

// MatchAny reports whether any of the patterns matches the module
// path as MatchPrefix has it.
func MatchAny(patterns []string, modulePath string) bool {
	for _, pat := range patterns {
		if MatchPrefix(pat, modulePath) {
			return true
		}
	}
	return false
}
