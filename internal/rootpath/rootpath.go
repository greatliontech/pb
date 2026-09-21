// Package rootpath is the one home of the root-contained path rule
// every root-relative path field shares: a forward-slash path that is
// relative, never escapes its root through "..", and — for fields
// whose written spelling is contract — is already clean. Three fields
// carry the rule (a workspace's use entries, a generation entry's out
// directory, and a plugin response's file names); one validator keeps
// their containment judgments identical, so a spelling one site
// accepts can never escape at another.
//
// The two entry points differ only in whether cleaning is the
// caller's or the validator's: Check demands the written spelling,
// Clean normalizes first. Both hold the same invariant: an accepted
// value joined under its root resolves strictly inside that root.
// Contains judges two accepted, cleaned directories against each
// other — whether one lies within the other — the containment a
// module tree forbids between module roots.
package rootpath

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Check accepts s exactly as written: non-empty, relative, clean, and
// never escaping the root. root names the root in error text ("the
// resolution root").
func Check(s, root string) error {
	if err := relative(s, root); err != nil {
		return err
	}
	// path.Clean("") is "." — an empty value fails here as well as
	// above, so neither check depends on the other's ordering.
	if path.Clean(s) != s {
		return fmt.Errorf("%q is not a clean path", s)
	}
	return escapes(s, s, root)
}

// Clean accepts s after normalizing it: non-empty, relative, and never
// escaping the root once cleaned. It returns the cleaned path, "." for
// the root itself.
func Clean(s, root string) (string, error) {
	if err := relative(s, root); err != nil {
		return "", err
	}
	c := path.Clean(s)
	if err := escapes(s, c, root); err != nil {
		return "", err
	}
	return c, nil
}

func relative(s, root string) error {
	if s == "" {
		return errors.New("empty path")
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("%q is absolute; paths are relative to %s", s, root)
	}
	return nil
}

// escapes judges the cleaned form c of the written s: a clean relative
// path escapes its root exactly when it is ".." or starts with "../".
func escapes(s, c, root string) error {
	if c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("%q escapes %s", s, root)
	}
	return nil
}

// Contains reports whether one cleaned root-relative directory lies
// within another: "." holds every other, a directory those beneath
// it, and none itself.
func Contains(outer, inner string) bool {
	return outer != inner && (outer == "." || strings.HasPrefix(inner, outer+"/"))
}
