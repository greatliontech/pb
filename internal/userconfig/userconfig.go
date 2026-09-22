// Package userconfig reads pb's machine-scoped settings
// (docs/specs/user-config.md): the user configuration file, and the
// resolution of each setting by layer — the environment over the
// file over nothing, the verb's flag being the caller's to rank
// above both. A resolved value carries the layer that stated it, so
// a refusal downstream can be attributed to that layer.
package userconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/greatliontech/pb/internal/contractfile"
)

// ErrInvalid wraps every refusal of the file's content.
var ErrInvalid = errors.New("invalid user configuration")

// FileName is the file's name under pb's directory in the user
// configuration directory.
const FileName = "config.yaml"

// Key names a setting: its key in the user configuration file.
type Key string

// The settings (docs/specs/user-config.md, the setting term).
const (
	KeyRunner      Key = "runner"
	KeyProxy       Key = "proxy"
	KeyNoproxy     Key = "noproxy"
	KeyCache       Key = "cache"
	KeyTrustedRoot Key = "trustedroot"
	KeyPluginPull  Key = "plugin-pull"
	KeyNetrc       Key = "netrc"
	KeySSH         Key = "ssh"
)

// Setting is what the resolver knows of one setting beyond its key:
// the environment variable that names it, and whether its value is a
// filesystem path, which the file states relative to its own
// directory.
type Setting struct {
	Env  string
	Path bool
}

// Keys are the settings, by key.
var Keys = map[Key]Setting{
	KeyRunner:      {Env: "PBRUNNER"},
	KeyProxy:       {Env: "PBPROXY"},
	KeyNoproxy:     {Env: "PBNOPROXY"},
	KeyCache:       {Env: "PBCACHE", Path: true},
	KeyTrustedRoot: {Env: "PBTRUSTEDROOT", Path: true},
	KeyPluginPull:  {Env: "PBPLUGINPULL"},
	KeyNetrc:       {Env: "PBNETRC", Path: true},
	KeySSH:         {Env: "PBSSH"},
}

// Path is the user configuration file's location on this host; the
// error is the platform's for a host with no user configuration
// directory (no HOME on Unix), or one whose stated location cannot
// be used (a relative XDG_CONFIG_HOME).
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("userconfig: resolving the user configuration directory: %w", err)
	}
	return filepath.Join(dir, "pb", FileName), nil
}

// LocationVar is the environment variable that states the user
// configuration directory's location on this platform — XDG_CONFIG_HOME
// where the platform reads it — and empty where the platform derives
// the directory from the home alone.
func LocationVar() string { return locationVar(runtime.GOOS) }

// locationVar is os.UserConfigDir's own platform table: the home
// alone on darwin, ios, windows and plan9, XDG_CONFIG_HOME everywhere
// else.
func locationVar(goos string) string {
	switch goos {
	case "darwin", "ios", "windows", "plan9":
		return ""
	}
	return "XDG_CONFIG_HOME"
}

// Settings are the file's values, by key, and the environment they
// are resolved beneath.
type Settings struct {
	path   string
	values map[Key]string
	getenv func(string) string
}

// Load reads the user configuration file at its location. An absent
// file is an empty configuration, and so is a host on which the file
// has no location because no home is defined: the user configuration
// directory is the file layer's alone to need, and its absence must
// not defeat a value the environment states. A location the user
// stated and the platform cannot use is a failure, never a silently
// ignored file.
func Load() (*Settings, error) {
	p, err := Path()
	if err != nil {
		if v := LocationVar(); v != "" && os.Getenv(v) != "" {
			return nil, err
		}
		return &Settings{values: map[Key]string{}, getenv: os.Getenv}, nil
	}
	return LoadFile(p, os.Getenv)
}

// LoadFile reads the file at path under the environment getenv reads
// (REQ-uc-format): an absent file is empty; anything present must be
// one mapping of setting keys to non-empty string scalars.
func LoadFile(path string, getenv func(string) string) (*Settings, error) {
	s := &Settings{path: path, values: map[Key]string{}, getenv: getenv}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("userconfig: %s: %w", path, err)
	}
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	if mapping == nil {
		return s, nil
	}
	for _, kv := range mapping.Values {
		// Admissibility (contractfile.Doc) admits string keys only.
		key := Key(kv.Key.(*ast.StringNode).Value)
		if _, known := Keys[key]; !known {
			return nil, fmt.Errorf("%w: %s: unknown setting %q (settings: %s)", ErrInvalid, path, key, strings.Join(sortedKeys(), ", "))
		}
		val, ok := kv.Value.(*ast.StringNode)
		if !ok || val.Value == "" {
			return nil, fmt.Errorf("%w: %s: %s must be a non-empty string", ErrInvalid, path, key)
		}
		s.values[key] = val.Value
	}
	return s, nil
}

// Layers a value can come from, as a refusal names them.
const (
	FromEnvironment = "the %s environment variable"
	FromFile        = "the user configuration file %s"
	FromDefault     = "the platform default"
)

// Defaulted is a setting's own default as a resolved value: the
// lowest layer, named as such in a refusal of it.
func Defaulted(v string) Value { return Value{Value: v, From: FromDefault} }

// Value is one setting as resolved (REQ-uc-precedence): the value,
// and the layer that stated it as a refusal names it — empty where no
// layer did, the value then being empty too.
type Value struct {
	Value string
	From  string
}

// Stated reports whether a layer stated the value.
func (v Value) Stated() bool { return v.From != "" }

// Wrap attributes a refusal of the value to the layer that stated it;
// a value no layer stated is refused as it was.
func (v Value) Wrap(err error) error {
	if !v.Stated() {
		return err
	}
	return fmt.Errorf("%s: %w", v.From, err)
}

// Get resolves one setting beneath the verb's flag
// (REQ-uc-precedence): the environment variable where set to a
// non-empty value, else the file's key where present, else nothing. A
// path the file states relative is resolved against the file's
// directory (REQ-uc-paths).
func (s *Settings) Get(key Key) Value {
	def, known := Keys[key]
	if !known {
		panic("userconfig: " + string(key) + " names no setting")
	}
	if v := s.getenv(def.Env); v != "" {
		return Value{Value: v, From: fmt.Sprintf(FromEnvironment, def.Env)}
	}
	if v := s.values[key]; v != "" {
		if def.Path && !filepath.IsAbs(v) {
			v = filepath.Join(filepath.Dir(s.path), v)
		}
		return Value{Value: v, From: fmt.Sprintf(FromFile, s.path)}
	}
	return Value{}
}

func sortedKeys() []string {
	keys := make([]string, 0, len(Keys))
	for k := range Keys {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}
