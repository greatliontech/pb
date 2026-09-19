package userconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/contractfile"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The settings are exactly the spec's, each bound to its environment
// variable, the two path-valued ones marked as such.
func TestKeys(t *testing.T) {
	want := map[Key]Setting{
		"runner":      {Env: "PBRUNNER"},
		"proxy":       {Env: "PBPROXY"},
		"noproxy":     {Env: "PBNOPROXY"},
		"cache":       {Env: "PBCACHE", Path: true},
		"trustedroot": {Env: "PBTRUSTEDROOT", Path: true},
		"plugin-pull": {Env: "PBPLUGINPULL"},
	}
	if !reflect.DeepEqual(Keys, want) {
		t.Fatalf("Keys = %v", Keys)
	}
	if KeyRunner != "runner" || KeyProxy != "proxy" || KeyNoproxy != "noproxy" || KeyCache != "cache" || KeyTrustedRoot != "trustedroot" || KeyPluginPull != "plugin-pull" {
		t.Fatal("the key constants drifted from the spec's keys")
	}
}

// The file is one flat mapping of known settings to non-empty
// strings; an absent file is empty; anything else is refused naming
// the file and the key (REQ-uc-format); a file that cannot be read is
// a failure, never an empty configuration.
func TestLoadFile(t *testing.T) {
	p := write(t, "runner: docker\ncache: /var/cache/pb\n")
	s, err := LoadFile(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get(KeyRunner); v != (Value{"docker", "the user configuration file " + p}) {
		t.Fatalf("runner = %+v", v)
	}
	if v := s.Get(KeyProxy); v.Stated() || v.Value != "" {
		t.Fatalf("unset setting = %+v", v)
	}
	absent, err := LoadFile(filepath.Join(t.TempDir(), FileName), env(nil))
	if err != nil {
		t.Fatalf("absent file: %v", err)
	}
	if v := absent.Get(KeyCache); v.Stated() {
		t.Fatal("an absent file yielded a value")
	}
	empty, err := LoadFile(write(t, "# nothing yet\n"), env(nil))
	if err != nil || len(empty.values) != 0 {
		t.Fatalf("empty file: %v %v", empty, err)
	}
	dir := t.TempDir()
	if _, err := LoadFile(dir, env(nil)); err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("unreadable file: %v", err)
	}
	if _, err := LoadFile(write(t, "runner: &a docker\ncache: *a\n"), env(nil)); !errors.Is(err, contractfile.ErrForbiddenNode) {
		t.Fatalf("an admissibility refusal is not matchable: %v", err)
	}
	for _, c := range []struct{ content, text string }{
		{"runnr: docker\n", `unknown setting "runnr" (settings: cache, noproxy, plugin-pull, proxy, runner, trustedroot)`},
		{"runner: [docker]\n", "runner must be a non-empty string"},
		{"runner: \"\"\n", "runner must be a non-empty string"},
		{"runner: docker\nrunner: native\n", `mapping key "runner" already defined`},
		{"- docker\n", "top level must be a mapping"},
		{"runner: &a docker\ncache: *a\n", "forbidden"},
		{"runner: docker\nproxy:\n  x: y\n", "proxy must be a non-empty string"},
	} {
		_, err := LoadFile(write(t, c.content), env(nil))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%q: %v, want ErrInvalid naming %q", c.content, err, c.text)
		}
	}
}

// The environment, where set to a non-empty value, wins over the
// file; an empty variable is an absent layer; each value names its
// layer, and a refusal wrapped by the value names it too
// (REQ-uc-precedence).
func TestGetPrecedence(t *testing.T) {
	p := write(t, "runner: docker\nproxy: https://proxy.example\n")
	s, err := LoadFile(p, env(map[string]string{"PBRUNNER": "native", "PBPROXY": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get(KeyRunner); v != (Value{"native", "the PBRUNNER environment variable"}) {
		t.Fatalf("runner = %+v", v)
	}
	v := s.Get(KeyProxy)
	if v != (Value{"https://proxy.example", "the user configuration file " + p}) {
		t.Fatalf("proxy = %+v", v)
	}
	refused := errors.New("no good")
	if err := v.Wrap(refused); !errors.Is(err, refused) || err.Error() != "the user configuration file "+p+": no good" {
		t.Fatalf("wrapped refusal: %v", err)
	}
	if err := s.Get(KeyNoproxy).Wrap(refused); err != refused {
		t.Fatalf("an unstated value's refusal was rewritten: %v", err)
	}
	if d := Defaulted("/var/cache/pb"); !d.Stated() || d.Value != "/var/cache/pb" || d.Wrap(refused).Error() != "the platform default: no good" {
		t.Fatalf("the default layer: %+v, %v", d, d.Wrap(refused))
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a key naming no setting was resolved")
		}
	}()
	s.Get("colour")
}

// A path-valued setting the file states relative is resolved against
// the file's directory; the environment's is taken as given, and
// absolute paths are taken as given from either (REQ-uc-paths).
func TestGetPaths(t *testing.T) {
	p := write(t, "cache: mod\ntrustedroot: /etc/pb/root.json\nproxy: relative/is/not/a/path\n")
	s, err := LoadFile(p, env(map[string]string{"PBTRUSTEDROOT": "root.json"}))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get(KeyCache); v.Value != filepath.Join(filepath.Dir(p), "mod") {
		t.Fatalf("cache = %+v", v)
	}
	if v := s.Get(KeyTrustedRoot); v.Value != "root.json" {
		t.Fatalf("trusted root from the environment = %+v", v)
	}
	if v := s.Get(KeyProxy); v.Value != "relative/is/not/a/path" {
		t.Fatalf("proxy = %+v", v)
	}
	s, err = LoadFile(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get(KeyTrustedRoot); v.Value != "/etc/pb/root.json" {
		t.Fatalf("absolute trusted root = %+v", v)
	}
}

// The platform table behind LocationVar is Go's own for
// os.UserConfigDir: XDG_CONFIG_HOME everywhere but darwin, ios,
// windows and plan9, this host included — pinned here because the
// tests that exercise the location rule skip on its word.
func TestLocationVar(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin": "", "ios": "", "windows": "", "plan9": "",
		"linux": "XDG_CONFIG_HOME", "freebsd": "XDG_CONFIG_HOME", "openbsd": "XDG_CONFIG_HOME",
		"android": "XDG_CONFIG_HOME", "solaris": "XDG_CONFIG_HOME", "wasip1": "XDG_CONFIG_HOME",
	} {
		if got := locationVar(goos); got != want {
			t.Errorf("locationVar(%s) = %q, want %q", goos, got, want)
		}
	}
	if got := LocationVar(); got != locationVar(runtime.GOOS) {
		t.Fatalf("LocationVar on %s = %q", runtime.GOOS, got)
	}
	// The variable named is the one Go reads: relocating it moves
	// the directory.
	if v := LocationVar(); v != "" {
		dir := t.TempDir()
		t.Setenv(v, dir)
		if p, err := Path(); err != nil || !strings.HasPrefix(p, dir) {
			t.Fatalf("Path = %q, %v under %s=%s", p, err, v, dir)
		}
	}
}

// The file lives under pb in the user configuration directory, which
// XDG_CONFIG_HOME relocates on Unix; a host with no home has no file,
// and the environment still resolves; a stated location the platform
// cannot use is a failure, never a silently ignored file.
func TestPath(t *testing.T) {
	if LocationVar() == "" {
		t.Skip("the user configuration directory is not relocatable by environment on this platform")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "pb", FileName) {
		t.Fatalf("Path = %q", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("cache: /tmp/pbcache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PBCACHE", "")
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Get(KeyCache); v.Value != "/tmp/pbcache" {
		t.Fatalf("cache = %+v", v)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("PBCACHE", "/tmp/fromenv")
	if _, err := Path(); err == nil {
		t.Fatal("a host with no HOME located the file")
	}
	s, err = Load()
	if err != nil {
		t.Fatalf("no configuration directory: %v", err)
	}
	if v := s.Get(KeyCache); v != (Value{"/tmp/fromenv", "the PBCACHE environment variable"}) {
		t.Fatalf("cache = %+v", v)
	}
	t.Setenv("XDG_CONFIG_HOME", "rel/ative")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "$XDG_CONFIG_HOME") {
		t.Fatalf("a stated location the platform cannot use was ignored: %v", err)
	}
}
