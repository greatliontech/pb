// Package local resolves and pins local-scheme plugins
// (plugin-execution.md, "Local binaries"): a host command named
// exactly as written, found on PATH or relative to the resolution
// root, run with its arguments verbatim from the resolution root,
// identified by its content hash per host platform and pinned in the
// lockfile unless the trust policy disables local pinning, and
// executed as a copy of the run's own, so the bytes run are the
// bytes hashed. It is the explicit downgrade the policy accepts: no
// manifest, no provenance, no image root.
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
	"strconv"
	"strings"
	"time"

	"github.com/greatliontech/pb/internal/flock"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Acquirer resolves local plugins against a resolution root and pins
// them in a lockfile under a trust policy.
type Acquirer struct {
	Root string // the resolution root as an absolute host path; anything else is refused
	// Runs is the directory the run's own is made under, as an
	// absolute host path: the copies the run executes live there
	// (REQ-plugin-local-pin), beside the plugin store.
	Runs   string
	Lock   *lockfile.File
	Policy *trust.Policy
	// run is the run's directory, made at its first acquisition and
	// removed by Release; copies counts the copies made into it.
	run    *run
	copies int
	// mkdirTemp makes the run's directory under Runs, removeAll
	// removes a directory whole; nil means os.MkdirTemp, os.RemoveAll.
	mkdirTemp func(dir, pattern string) (string, error)
	removeAll func(path string) error
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

// Acquire resolves the command, copies it into the run's directory
// and pins it (REQ-plugin-local-pin): the binary's content hash on
// this platform is recorded on first use and checked on every later
// one, naming both hashes on a mismatch; a binary that moved with the
// same bytes is a non-event. The process is the copy with args after
// it, verbatim, run from the resolution root
// (REQ-plugin-local-resolution): the copy is hashed as it is written,
// so the bytes the run executes are the bytes the pin names, whatever
// is rebuilt at the resolved place meanwhile. The pin is the
// command's alone, whatever the arguments. A policy disabling local
// pinning records and checks nothing; the copy is made all the same.
func (a *Acquirer) Acquire(ctx context.Context, value string, args []string) (*plugin.Acquired, error) {
	resolved, err := a.Resolve(value)
	if err != nil {
		return nil, err
	}
	r, err := a.begin()
	if err != nil {
		return nil, err
	}
	a.copies++
	path, hash, err := r.copy(ctx, resolved, a.copies)
	if err != nil {
		return nil, fmt.Errorf("local: copying %s: %w", resolved, err)
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

// lockName is the file a run's directory is held by while the run
// lives: its lock, taken at the directory's making and released with
// its removal, is what tells a live run's directory from residue. A
// remover takes the directory out of a claim's reach before removing
// anything: it condemns the lock file under the lock — a byte written
// into it, so a run finding the lock free on a condemned file knows
// the directory is a remover's — and renames the directory to its
// tombstone, which no claim names, before the removal, so nothing is
// made inside what is being removed.
const lockName = "lock"

// runPrefix names a run's directory under Runs, deadPrefix the
// tombstone a remover renames it to: a claim names the former alone,
// a sweep removes both.
const runPrefix, deadPrefix = "run-", "dead-"

// tombstone is the name a remover gives the run's directory before
// removing it.
func tombstone(dir string) string {
	return filepath.Join(filepath.Dir(dir), deadPrefix+strings.TrimPrefix(filepath.Base(dir), runPrefix))
}

// run is one generation's directory of copies under Runs, held by
// its lock while the run lives; rm removes a directory whole.
type run struct {
	dir  string
	lock *os.File
	rm   func(path string) error
}

// begin is the run's directory, made under Runs at the first
// acquisition: a fresh directory, its lock taken, the residue of
// earlier runs swept. A claim is decided by the lock alone: a fresh
// directory a sweeper reached first — the lock taken and the
// directory condemned between its making and the run's own taking —
// is left to that sweeper and another made.
func (a *Acquirer) begin() (*run, error) {
	if a.run != nil {
		return a.run, nil
	}
	if !filepath.IsAbs(a.Runs) {
		return nil, fmt.Errorf("local: the runs directory %q is not an absolute host path", a.Runs)
	}
	if err := os.MkdirAll(a.Runs, 0o755); err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	mkdirTemp, rm := a.mkdirTemp, a.removeAll
	if mkdirTemp == nil {
		mkdirTemp = os.MkdirTemp
	}
	if rm == nil {
		rm = os.RemoveAll
	}
	for attempt := 0; ; attempt++ {
		dir, err := mkdirTemp(a.Runs, runPrefix)
		if err != nil {
			return nil, fmt.Errorf("local: %w", err)
		}
		f, claimed, err := claim(dir)
		if err != nil {
			_ = rm(dir)
			return nil, err
		}
		if !claimed {
			if attempt == 2 {
				return nil, fmt.Errorf("local: the run's directory under %s swept from under it three times", a.Runs)
			}
			continue
		}
		if err := sweep(a.Runs, dir, false, rm); err != nil {
			_ = (&run{dir: dir, lock: f, rm: rm}).remove(true)
			return nil, err
		}
		a.run = &run{dir: dir, lock: f, rm: rm}
		return a.run, nil
	}
}

// claim takes the directory's lock for a run of its own: the lock
// file made where missing, the lock taken without waiting, and the
// claim refused where the lock is held or the file is condemned — a
// sweeper's, the directory gone or going — or the directory is gone
// already.
func claim(dir string) (*os.File, bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o644)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("local: %w", err)
	}
	taken, err := flock.TryLock(f)
	if err != nil {
		f.Close()
		return nil, false, fmt.Errorf("local: %w", err)
	}
	if !taken {
		f.Close()
		return nil, false, nil
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, false, fmt.Errorf("local: %w", err)
	}
	if fi.Size() > 0 {
		f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

// Release removes the run's directory with every copy in it,
// releasing its lock; a run that made none has nothing to remove.
func (a *Acquirer) Release() error {
	if a.run == nil {
		return nil
	}
	r := a.run
	a.run = nil
	return r.remove(true)
}

// Sweep removes the residue under runs of runs that ended without
// removing their directory — a crash's — and keeps a live run's,
// which its lock holds, failing on residue it cannot remove
// (REQ-plugin-local-pin, dep-verbs.md REQ-dep-clean); a runs
// directory that does not exist holds none.
func Sweep(runs string) error {
	return sweep(runs, "", true, os.RemoveAll)
}

// sweep is Sweep sparing the directory at except, the sweeping run's
// own, and — where not strict, at a run's start — leaving residue it
// cannot remove to a later sweep rather than failing the run, tried
// once. A run's directory and a remover's tombstone are swept, a
// stranger under Runs is not. A directory whose lock file is
// missing — a run that ended between its directory's making and its
// lock's, a remover's between its unlinking and the directory's — is
// residue the sweeper makes the lock file for and takes.
func sweep(runs, except string, strict bool, rm func(string) error) error {
	entries, err := os.ReadDir(runs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("local: %w", err)
	}
	for _, e := range entries {
		dir := filepath.Join(runs, e.Name())
		if !e.IsDir() || dir == except || !(strings.HasPrefix(e.Name(), runPrefix) || strings.HasPrefix(e.Name(), deadPrefix)) {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o644)
		if errors.Is(err, os.ErrNotExist) {
			// Removed by another sweeper or its own run meanwhile.
			continue
		}
		if err == nil {
			var taken bool
			if taken, err = flock.TryLock(f); err == nil && !taken {
				f.Close()
				continue
			}
			if err == nil {
				err = (&run{dir: dir, lock: f, rm: rm}).remove(strict)
			} else {
				f.Close()
				err = fmt.Errorf("local: %w", err)
			}
		} else {
			err = fmt.Errorf("local: %w", err)
		}
		if err != nil && strict {
			return err
		}
	}
	return nil
}

// copy writes the binary at src into the run's directory as the n-th
// copy, under its own name, hashing the bytes as they are written:
// the copy's path and the hash of its bytes. The copy carries the
// source's permissions less the write bits, its executable bits
// among them: what was hashed is not written to again.
func (r *run) copy(ctx context.Context, src string, n int) (string, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(r.dir, strconv.Itoa(n))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm()&^0o222)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return "", "", err
		}
		n, err := in.Read(buf)
		if _, werr := out.Write(buf[:n]); werr != nil {
			out.Close()
			return "", "", werr
		}
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			out.Close()
			return "", "", err
		}
	}
	if err := out.Close(); err != nil {
		return "", "", err
	}
	return dst, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// remove takes the run's directory out of a claim's reach, releases
// its lock and removes it with every copy in it: the lock file
// condemned under the lock, so no run claims the file once the lock
// is free; the lock released and its file closed — a locked open
// file cannot be removed on windows; the directory renamed to its
// tombstone, which no claim names, so nothing is made inside what is
// removed next, and the tombstone's lock file checked to be
// condemned, a directory another run made anew under the name
// meanwhile — its lock file empty — renamed back; a rename finding
// nothing at the name leaves the directory to the remover that
// renamed it first — a tombstone already, a remover's that ended
// before the removal, is removed as it is; then the tombstone
// removed. The
// rename and the removal are tried again briefly where asked and
// failing while the directory stands — another remover holding the
// lock file open in the instant between its taking and its close —
// and what another remover did already is tolerated.
func (r *run) remove(retry bool) error {
	if _, err := r.lock.WriteAt([]byte{'x'}, 0); err != nil {
		r.lock.Close()
		return fmt.Errorf("local: %w", err)
	}
	if err := flock.Unlock(r.lock); err != nil {
		r.lock.Close()
		return fmt.Errorf("local: %w", err)
	}
	if err := r.lock.Close(); err != nil {
		return fmt.Errorf("local: %w", err)
	}
	dead := r.dir
	if strings.HasPrefix(filepath.Base(r.dir), runPrefix) {
		dead = tombstone(r.dir)
		renamed := false
		if err := attempt(retry, func() error {
			err := os.Rename(r.dir, dead)
			renamed = err == nil
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}); err != nil {
			return fmt.Errorf("local: %w", err)
		}
		if !renamed {
			// Not at its name: another remover renamed it first and
			// removes it, and no new life under the name can stand
			// while it is gone — nothing of this remover's to do.
			return nil
		}
		// The lock held names a file, the rename a directory: a
		// directory removed whole and made again under the same name
		// by a new run in between — the stdlib's fresh name reused —
		// is the new run's, told by its lock file: a claim's is
		// empty, and only a remover condemns one, under a lock a
		// claim holds. Renamed back, untouched. A lock file gone is
		// another remover's unlinking, of a tombstone it reached
		// after the rename, and the removal carries on; a new life
		// with no lock file yet, between its making and its claim,
		// is removed and its claim finds nothing, another made.
		// The look itself tried again where asked: a file another
		// program holds open for the instant of its unlinking.
		var fi os.FileInfo
		err := attempt(retry, func() error {
			var err error
			if fi, err = os.Stat(filepath.Join(dead, lockName)); errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		})
		switch {
		case fi != nil && fi.Size() == 0:
			if err := os.Rename(dead, r.dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("local: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("local: %w", err)
		}
	}
	if err := attempt(retry, func() error {
		err := r.rm(dead)
		if _, serr := os.Stat(dead); err != nil && errors.Is(serr, os.ErrNotExist) {
			return nil
		}
		return err
	}); err != nil {
		return fmt.Errorf("local: %w", err)
	}
	return nil
}

// attempt runs f, and where retry is asked, again up to five times
// at short intervals while it fails.
func attempt(retry bool, f func() error) error {
	attempts := 1
	if retry {
		attempts = 5
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return err
}
