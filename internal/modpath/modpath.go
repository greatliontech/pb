// Package modpath validates module paths (REQ-resolve-path-syntax): a
// hostname followed by one or more segments, with a character discipline
// that keeps paths safe in every downstream encoding — proxy bang-escaping
// (ASCII case), vanity-redirect URLs, and plain-scalar YAML emission.
package modpath

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is wrapped by every module-path rejection.
var ErrInvalid = errors.New("invalid module path")

// Validate checks path against the module-path syntax: a hostname
// (lowercase ASCII letters, digits, hyphens, dots, at least one interior
// dot) followed by one or more segments of ASCII letters, digits, and
// . - _ ~, no segment beginning or ending with a dot.
func Validate(path string) error {
	host, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		return fmt.Errorf("%w: %q needs a hostname followed by at least one segment", ErrInvalid, path)
	}
	if err := validateHost(host); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalid, path, err)
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if err := validateSegment(seg); err != nil {
			return fmt.Errorf("%w: %q: %v", ErrInvalid, path, err)
		}
	}
	return nil
}

// validateHost enforces DNS label discipline: two or more dot-separated
// non-empty labels of lowercase letters, digits, and hyphens, no label
// beginning or ending with a hyphen — a host that could never resolve is
// rejected here instead of failing confusingly at probe time.
func validateHost(host string) error {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return errors.New("hostname needs at least two dot-separated labels")
	}
	for _, label := range labels {
		if label == "" {
			return errors.New("hostname has an empty label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("hostname label begins or ends with a hyphen")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return fmt.Errorf("hostname contains %q", c)
			}
		}
	}
	return nil
}

func validateSegment(seg string) error {
	if seg == "" {
		return errors.New("empty segment")
	}
	if seg[0] == '.' || seg[len(seg)-1] == '.' {
		return errors.New("segment begins or ends with a dot")
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == '~':
		default:
			return fmt.Errorf("segment contains %q", c)
		}
	}
	return nil
}
