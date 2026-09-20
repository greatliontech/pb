// Package pluglocal resolves and pins local-scheme plugins
// (plugin-execution.md, "Local binaries"): a host binary named
// exactly as written, found on PATH or relative to the resolution
// root, identified by its content hash per host platform and pinned
// in the lockfile unless the trust policy disables local pinning. It
// is the explicit downgrade the policy accepts: no manifest, no
// provenance, no image root.
package pluglocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Acquired is a resolved local plugin: the process to run — the
// binary at its host path, alone, with the host's environment and
// working directory — and its pin.
type Acquired struct {
	Process plugexec.Process
	Pin     lockfile.PluginPin
}

// Acquirer resolves local plugins against a resolution root and pins
// them in a lockfile under a trust policy.
type Acquirer struct {
	Root   string // the resolution root as an absolute host path; anything else is refused
	Lock   *lockfile.File
	Policy *trust.Policy
	// platform keys the pin; empty means the running host's.
	platform string
	// lookPath finds a bare name on PATH; nil means exec.LookPath.
	lookPath func(string) (string, error)
}

// Platform is the pin key for the running host.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Resolve finds the binary a local value names
// (REQ-plugin-local-resolution): a value with no path separator is
// looked up on PATH exactly as written; one with a separator is a
// path relative to the resolution root, absolute allowed, written
// with forward slashes. A failure names the search; nothing else is
// tried.
func (a *Acquirer) Resolve(value string) (string, error) {
	if !filepath.IsAbs(a.Root) {
		return "", fmt.Errorf("pluglocal: the resolution root %q is not an absolute host path", a.Root)
	}
	if !strings.Contains(value, "/") {
		look := a.lookPath
		if look == nil {
			look = exec.LookPath
		}
		p, err := look(value)
		if err != nil {
			return "", fmt.Errorf("pluglocal: %q not found on PATH (looked up exactly as written): %v", value, err)
		}
		return p, nil
	}
	p := filepath.FromSlash(value)
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.Root, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("pluglocal: %q resolved relative to %s: %v", value, a.Root, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("pluglocal: %q resolved to %s, which is not a regular file", value, p)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("pluglocal: %q resolved to %s, which is not executable", value, p)
	}
	return p, nil
}

// Acquire resolves the value and pins it (REQ-plugin-local-pin): the
// binary's content hash on this platform is recorded on first use
// and checked on every later one, naming both hashes on a mismatch;
// a binary that moved with the same bytes is a non-event. A policy
// disabling local pinning records and checks nothing.
func (a *Acquirer) Acquire(ctx context.Context, value string) (*Acquired, error) {
	path, err := a.Resolve(value)
	if err != nil {
		return nil, err
	}
	hash, err := hashFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("pluglocal: hashing %s: %w", path, err)
	}
	acq := &Acquired{Process: plugexec.Process{Argv: []string{path}}}
	if a.Policy != nil && !a.Policy.Execution.LocalPinEnabled() {
		return acq, nil
	}
	platform := a.platform
	if platform == "" {
		platform = Platform()
	}
	pin, ok := a.Lock.Plugin(value, lockfile.SchemeLocal)
	switch {
	case !ok:
		pin = lockfile.PluginPin{Ref: value, Scheme: lockfile.SchemeLocal, Binary: map[string]string{platform: hash}}
		if err := a.Lock.AddPlugin(pin); err != nil {
			return nil, err
		}
	default:
		if err := a.Lock.SetPluginBinary(value, platform, hash); err != nil {
			return nil, err
		}
		pin, _ = a.Lock.Plugin(value, lockfile.SchemeLocal)
	}
	acq.Pin = pin
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
