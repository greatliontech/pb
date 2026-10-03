// Package module is the module domain's root: the module path rule
// (REQ-resolve-path-syntax) every package of the domain and every
// consumer spells a path through. A path is a hostname followed by one
// or more segments, with a character discipline that keeps it safe in
// every downstream encoding — proxy bang-escaping (ASCII case),
// vanity-redirect URLs, and plain-scalar YAML emission. The domain's
// other rules — the archive, versions, the module file, selection, the
// workspace, the lockfile — are its subpackages.
package module

import (
	"errors"
	"fmt"
	"strings"
)

// ModuleFileName is the module file's name at the module root — the
// module-archive contract's "module file" term. The schema is
// modfile's; a pb.yaml declared anywhere strictly below the root
// invalidates an archive's file set (REQ-archive-nested-module).
const ModuleFileName = "pb.yaml"

// RuleFileSuffix names a rule file: every file so named under a
// module's root, at any depth, is one of the module's rule files
// (check-rules.md REQ-rules-file-discovery); the module loader
// carries them beside the protobuf files.
const RuleFileSuffix = ".rules.yaml"

// ProtoFileSuffix names a protobuf file: every file so named under a
// module's root, at any depth outside a nested module, is one of the
// module's files.
const ProtoFileSuffix = ".proto"

// IsProtoFile and IsRuleFile judge a file by its name — the one
// predicate every walk over a module's files applies, from the
// working tree, an archive, or a repository's tree alike.
func IsProtoFile(name string) bool { return strings.HasSuffix(name, ProtoFileSuffix) }

// IsRuleFile reports whether a file name names a rule file.
func IsRuleFile(name string) bool { return strings.HasSuffix(name, RuleFileSuffix) }

// ErrInvalidPath is wrapped by every module-path rejection.
var ErrInvalidPath = errors.New("invalid module path")

// ValidateSubtree checks a module's subtree within its repository as
// an origin names it: a clean relative path of module path segments,
// "" for the repository root.
func ValidateSubtree(subtree string) error {
	if subtree == "" {
		return nil
	}
	for _, seg := range strings.Split(subtree, "/") {
		if err := validateSegment(seg); err != nil {
			return fmt.Errorf("subtree %q: %w", subtree, err)
		}
	}
	return nil
}

// ValidatePath checks path against the module-path syntax: a hostname
// (lowercase ASCII letters, digits, hyphens, dots, at least one interior
// dot) followed by one or more segments of ASCII letters, digits, and
// . - _ ~, no segment beginning or ending with a dot.
func ValidatePath(path string) error {
	host, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		return fmt.Errorf("%w: %q needs a hostname followed by at least one segment", ErrInvalidPath, path)
	}
	if err := validateHost(host); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidPath, path, err)
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if err := validateSegment(seg); err != nil {
			return fmt.Errorf("%w: %q: %v", ErrInvalidPath, path, err)
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
