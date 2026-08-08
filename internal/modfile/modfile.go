// Package modfile parses, validates, and canonically emits the module file
// (pb.yaml) — the distribution contract: module identity plus declared
// dependencies, and nothing else. Parsing goes through the YAML AST so the
// accepted surface is the schema's, not the parser's: exactly one document,
// no merge keys, deps present only as a non-empty mapping. Emission is
// hand-rolled because the byte form is contractual (REQ-modfile-emission)
// and must not drift with a marshaller. Validated module paths and versions
// are plain-scalar-safe, so the emitter never needs quoting.
package modfile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"golang.org/x/mod/semver"

	"github.com/greatliontech/pb/internal/modpath"
	"github.com/greatliontech/pb/internal/yamlshape"
)

// ErrInvalid is wrapped by every module-file rejection other than an
// identity mismatch.
var ErrInvalid = errors.New("invalid module file")

// ErrIdentityMismatch is wrapped when a module file's declared path differs
// from the path the module is required and fetched under
// (REQ-modfile-identity).
var ErrIdentityMismatch = errors.New("module identity mismatch")

// File is a parsed module file: the module path and the declared
// dependencies (module path -> minimum required version).
type File struct {
	Module string
	Deps   map[string]string
}

type rawFile struct {
	Module string            `yaml:"module"`
	Deps   map[string]string `yaml:"deps"`
}

// Parse decodes and validates module-file bytes (REQ-modfile-schema):
// exactly one YAML document whose top level is a mapping with exactly the
// keys module and — only when dependencies exist — deps; no merge keys
// (their expansion differs between YAML 1.1 and 1.2 parsers, and a
// content-addressed contract file must read identically everywhere). The
// module value is a valid module path, every dependency key a valid module
// path, and every dependency value a valid version (REQ-modfile-versions).
func Parse(data []byte) (*File, error) {
	astFile, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(astFile.Docs) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one YAML document, found %d", ErrInvalid, len(astFile.Docs))
	}
	body := astFile.Docs[0].Body
	if body == nil {
		return nil, fmt.Errorf("%w: missing module key", ErrInvalid)
	}
	mapping, ok := body.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("%w: top level must be a mapping", ErrInvalid)
	}
	if err := yamlshape.Check(mapping); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := checkMappingShape(mapping); err != nil {
		return nil, err
	}
	var raw rawFile
	if err := yaml.NodeToValue(body, &raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	f := &File{Module: raw.Module, Deps: raw.Deps}
	if err := validate(f); err != nil {
		return nil, err
	}
	return f, nil
}

// checkMappingShape enforces the schema at the AST level: keys are exactly
// module (+ deps), and deps — when present — is a non-empty mapping (a
// null or empty value is "present without dependencies", which the schema
// excludes). It runs after yamlshape.Check, so every key is a string node
// and comparisons see the unquoted key value.
func checkMappingShape(mapping *ast.MappingNode) error {
	for _, kv := range mapping.Values {
		key, ok := kv.Key.(*ast.StringNode)
		if !ok {
			return fmt.Errorf("%w: unknown key %q", ErrInvalid, kv.Key.String())
		}
		switch key.Value {
		case "module":
		case "deps":
			// Null, sequence, and scalar values all fail the mapping
			// assertion; an empty mapping decodes to a non-nil empty map
			// that validate rejects with the same message.
			if _, ok := kv.Value.(*ast.MappingNode); !ok {
				return fmt.Errorf("%w: deps must be a non-empty mapping", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: unknown key %q", ErrInvalid, key.Value)
		}
	}
	return nil
}

// validate checks a File's content against the schema; Parse accepts and
// Encode emits exactly the files that pass it.
func validate(f *File) error {
	if f.Module == "" {
		return fmt.Errorf("%w: missing module key", ErrInvalid)
	}
	if err := modpath.Validate(f.Module); err != nil {
		return fmt.Errorf("%w: module: %v", ErrInvalid, err)
	}
	if f.Deps != nil && len(f.Deps) == 0 {
		return fmt.Errorf("%w: deps must be a non-empty mapping", ErrInvalid)
	}
	for p, v := range f.Deps {
		if err := modpath.Validate(p); err != nil {
			return fmt.Errorf("%w: dep: %v", ErrInvalid, err)
		}
		if err := checkVersion(v); err != nil {
			return fmt.Errorf("%w: dep %q: %v", ErrInvalid, p, err)
		}
	}
	return nil
}

// checkVersion enforces REQ-modfile-versions: a full v-prefixed semver
// version, optionally with a prerelease (which covers pseudo-versions), and
// no build metadata. Canonical equality rejects shortened forms like v1.2.
func checkVersion(v string) error {
	if !semver.IsValid(v) || v != semver.Canonical(v) {
		return fmt.Errorf("version %q is not a canonical vX.Y.Z[-prerelease] version", v)
	}
	return nil
}

// Encode renders the file canonically (REQ-modfile-emission): UTF-8, LF,
// two-space indent, module first, deps sorted by key in raw-byte order.
// The file is validated first — Encode never emits what Parse rejects.
func Encode(f *File) ([]byte, error) {
	if err := validate(f); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("module: ")
	b.WriteString(f.Module)
	b.WriteByte('\n')
	if len(f.Deps) > 0 {
		b.WriteString("deps:\n")
		paths := make([]string, 0, len(f.Deps))
		for p := range f.Deps {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			b.WriteString("  ")
			b.WriteString(p)
			b.WriteString(": ")
			b.WriteString(f.Deps[p])
			b.WriteByte('\n')
		}
	}
	return []byte(b.String()), nil
}

// CheckIdentity enforces REQ-modfile-identity: the declared module path
// must equal the path the module is required and fetched under.
func CheckIdentity(f *File, required string) error {
	if f.Module != required {
		return fmt.Errorf("%w: file declares %q, required as %q", ErrIdentityMismatch, f.Module, required)
	}
	return nil
}
