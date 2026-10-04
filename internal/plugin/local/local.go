// Package local resolves local-scheme plugins (plugin-execution.md,
// "Local binaries"): a host command named exactly as written, found
// on PATH or relative to the resolution root, run with its arguments
// verbatim from the resolution root. Nothing of it is recorded: a
// host binary has no identity pb could resolve, so none is claimed.
// It is the explicit downgrade the trust policy accepts: no manifest,
// no provenance, no image root.
package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/greatliontech/pb/internal/plugin"
)

// Acquirer resolves local plugins against a resolution root.
type Acquirer struct {
	Root string // the resolution root as an absolute host path; anything else is refused
	// platform is the host resolved for; the zero value means the
	// running host's.
	platform plugin.Platform
	// lookPath finds a bare name on PATH; nil means exec.LookPath.
	// On windows the lookup is the acquirer's own (lookExact) and
	// reads PATH through env; nil means os.Getenv.
	lookPath func(string) (string, error)
	env      func(string) string
}

// Resolve finds the binary a local value names
// (REQ-plugin-local-resolution): a value with no path separator is
// looked up on PATH exactly as written — on windows as the name with
// `.exe` appended unless it already ends in `.exe`, found by that
// spelling alone in a PATH directory, never by the platform's other
// suffixes, a dropped trailing dot or the working directory
// (platforms.md REQ-plat-local-runner); one with a separator is a
// path relative to the resolution root, absolute allowed, written
// with forward slashes, a backslash being neither. A failure names
// the search; nothing else is tried.
func (a *Acquirer) Resolve(value string) (string, error) {
	if !filepath.IsAbs(a.Root) {
		return "", fmt.Errorf("local: the resolution root %q is not an absolute host path", a.Root)
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("local: %q holds a backslash: a path is spelled with forward slashes, a name holds none", value)
	}
	if !strings.Contains(value, "/") {
		if a.hostPlatform().OS == "windows" {
			// A name ending in a dot is refused rather than resolved to
			// the name the platform would drop it to.
			if strings.HasSuffix(value, ".") {
				return "", fmt.Errorf("local: %q ends in a dot, which names no file", value)
			}
			name := value
			if !strings.EqualFold(filepath.Ext(value), ".exe") {
				name += ".exe"
			}
			p, err := lookExact(name, a.getenv()("PATH"))
			if err != nil {
				return "", fmt.Errorf("local: %q not found on PATH (looked up exactly as %q): %v", value, name, err)
			}
			return p, nil
		}
		look := a.lookPath
		if look == nil {
			look = exec.LookPath
		}
		p, err := look(value)
		if err != nil {
			return "", fmt.Errorf("local: %q not found on PATH (looked up exactly as %q): %v", value, value, err)
		}
		return p, nil
	}
	p := filepath.FromSlash(value)
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.Root, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("local: %q resolved relative to %s: %v", value, a.Root, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("local: %q resolved to %s, which is not a regular file", value, p)
	}
	// windows has no executable bit: the platform decides at exec.
	if a.hostPlatform().OS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("local: %q resolved to %s, which is not executable", value, p)
	}
	return p, nil
}

// hostPlatform is the platform the acquirer resolves for: the one
// set, else the running host's.
func (a *Acquirer) hostPlatform() plugin.Platform {
	if a.platform != (plugin.Platform{}) {
		return a.platform
	}
	return plugin.HostPlatform()
}

// getenv reads the environment the lookup runs under: the one set,
// else the process's.
func (a *Acquirer) getenv() func(string) string {
	if a.env != nil {
		return a.env
	}
	return os.Getenv
}

// lookExact finds name in the directories of pathList, the platform's
// list separator between them, as a regular file spelled exactly so:
// no suffix appended, no spelling the platform would also accept
// (case being the filesystem's), no directory but the list's
// absolute ones — an empty or relative entry names the working
// directory, pb's wherever it was invoked, and is passed over.
func lookExact(name, pathList string) (string, error) {
	for _, dir := range filepath.SplitList(pathList) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", errors.New("no PATH directory holds it")
}

// Acquire resolves the command (REQ-plugin-local-resolution): the
// process is the resolved command with args after it, verbatim, run
// from the resolution root. Nothing is recorded of it — a host binary
// has no identity pb could resolve, no store serving its bytes and no
// manifest naming them, so a hash of it would be a fact one machine
// recorded and no other could satisfy; the scheme's permission in the
// trust policy is the root's acceptance of exactly that
// (plugin-execution.md, "Local binaries").
func (a *Acquirer) Acquire(value string, args []string) (*plugin.Acquired, error) {
	path, err := a.Resolve(value)
	if err != nil {
		return nil, err
	}
	return &plugin.Acquired{Process: plugin.Process{Argv: append([]string{path}, args...), WorkDir: a.Root}}, nil
}
