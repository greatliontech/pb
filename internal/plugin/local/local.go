// Package local resolves and pins local-scheme plugins
// (plugin-execution.md, "Local binaries"): a host command named
// exactly as written, found on PATH or relative to the resolution
// root, run with its arguments verbatim from the resolution root,
// identified by its content hash per host platform and pinned in the
// lockfile unless the trust policy disables local pinning. It is the
// explicit downgrade the policy accepts: no manifest, no provenance,
// no image root.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Acquirer resolves local plugins against a resolution root and pins
// them in a lockfile under a trust policy.
type Acquirer struct {
	Root   string // the resolution root as an absolute host path; anything else is refused
	Lock   *lockfile.File
	Policy *trust.Policy
	// platform keys the pin; the zero value means the running host's.
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

// hostPlatform is the platform the acquirer resolves and pins for:
// the one set, else the running host's.
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
// directory, which the pinned hash and the run would read
// differently, and is passed over.
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

// Acquire resolves the command and pins it (REQ-plugin-local-pin):
// the binary's content hash on this platform is recorded on first
// use and checked on every later one, naming both hashes on a
// mismatch; a binary that moved with the same bytes is a non-event.
// The process is the resolved command with args after it, verbatim,
// run from the resolution root (REQ-plugin-local-resolution); the
// pin is the command's alone, whatever the arguments. A policy
// disabling local pinning records and checks nothing.
func (a *Acquirer) Acquire(ctx context.Context, value string, args []string) (*plugin.Acquired, error) {
	path, err := a.Resolve(value)
	if err != nil {
		return nil, err
	}
	hash, err := hashFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("local: hashing %s: %w", path, err)
	}
	acq := &plugin.Acquired{Process: plugin.Process{Argv: append([]string{path}, args...), WorkDir: a.Root}}
	if a.Policy != nil && !a.Policy.Execution.LocalPinEnabled() {
		return acq, nil
	}
	platform := a.hostPlatform()
	_, ok := a.Lock.Plugin(value, lockfile.SchemeLocal)
	switch {
	case !ok:
		pin := lockfile.PluginPin{Ref: value, Scheme: lockfile.SchemeLocal, Binary: map[string]string{platform.String(): hash}}
		if err := a.Lock.AddPlugin(pin); err != nil {
			return nil, err
		}
	default:
		if err := a.Lock.SetPluginBinary(value, platform.String(), hash); err != nil {
			return nil, err
		}
	}
	return acq, nil
}

// hashFile is the binary's content hash, read under ctx.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
