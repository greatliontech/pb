package migrate

import (
	"fmt"
	"strings"

	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/rootpath"
)

// pluginEntry is one row of the plugin table: the image repository a
// BSR plugin's versions are published under and the versions built,
// ascending as versions, the highest last.
type pluginEntry struct {
	repo     string
	versions []string
}

// Plugins is the plugin table (migrate.md, the mapping table term):
// buf's plugins as greatliontech's fork of buf's plugin repository
// builds them, each version an image tagged with it. The spec's table
// is the same list, held to this one by TestPluginTableMatchesSpec,
// which holds each entry's versions ascending too.
var Plugins = map[string]pluginEntry{
	"buf.build/grpc/csharp":            {"ghcr.io/greatliontech/pbr-plugins/grpc/csharp", []string{"v1.68.2"}},
	"buf.build/grpc/go":                {"ghcr.io/greatliontech/pbr-plugins/grpc/go", []string{"v1.4.0", "v1.5.1"}},
	"buf.build/grpc/web":               {"ghcr.io/greatliontech/pbr-plugins/grpc/web", []string{"v1.4.2"}},
	"buf.build/protocolbuffers/csharp": {"ghcr.io/greatliontech/pbr-plugins/protocolbuffers/csharp", []string{"v29.2"}},
	"buf.build/protocolbuffers/go":     {"ghcr.io/greatliontech/pbr-plugins/protocolbuffers/go", []string{"v1.34.2", "v1.35.2"}},
	"buf.build/protocolbuffers/js":     {"ghcr.io/greatliontech/pbr-plugins/protocolbuffers/js", []string{"v3.21.2"}},
}

// keyReasons are the reasons a plugin entry's passed-over keys map to
// nothing (REQ-migrate-gen), by key.
var keyReasons = map[string]string{
	"strategy":        "pb generates over the workspace's own files under one strategy",
	"include_imports": "pb generates over the workspace's own files under one strategy",
	"include_wkt":     "pb generates over the workspace's own files under one strategy",
	"types":           "pb generates over the workspace's own files under one strategy",
	"exclude_types":   "pb generates over the workspace's own files under one strategy",
	"revision":        "pb pins a plugin by its image digest, not a build revision",
	"protoc_path":     "pb runs no protoc",
	"remote":          "buf's alpha remote plugin, run by the BSR: pb runs every plugin itself",
}

// computed are buf's managed-mode file options that are no protobuf
// option but a rule buf computes each file's value by: a prefix or
// suffix over the package.
var computed = map[string]bool{
	"go_package_prefix": true, "java_package_prefix": true, "java_package_suffix": true,
	"csharp_namespace_prefix": true, "php_metadata_namespace_suffix": true, "ruby_package_suffix": true,
}

// Gen builds the generation file from buf.gen.yaml (REQ-migrate-gen).
// Each plugin entry naming a BSR plugin is looked up among the
// replacements and then in the plugin table, a name either holds
// becoming a `ref` entry: the replacement's reference as given, or
// the table's repository at the version named where the fork builds
// it, at the highest built where none is named; a name neither
// holds, or a version the fork does not build, an unmapped fact
// naming the flag's form. A local entry naming one executable
// becomes a `local` entry, one naming a command with arguments an
// unmapped fact; a protoc builtin an unmapped fact; out as written
// where pb's schema takes it, an unmapped fact where not; opt as
// joined; each key the reader passed over an unmapped fact naming
// it, as is `inputs`. buf's managed mode becomes overrides where an
// entry is declarative — a file option with a value over every file
// or over the files a path names, the path relative to a file's
// module as buf matches it and pb's globs are; v1's boolean options,
// its `override` map of option to file to value, and the defaults of
// `optimize_for`, `objc_class_prefix` and `swift_prefix` — and an
// unmapped fact where
// it is buf's own heuristic or names a module, which a
// module-relative glob cannot. The file is set on the layout, nil
// where no plugin mapped, the managed mode then read for nothing,
// and the facts returned.
func Gen(gen *bufconfig.Gen, repl Replacements, l *Layout) ([]Fact, error) {
	if gen == nil || l == nil {
		return nil, fmt.Errorf("no buf.gen.yaml read")
	}
	// The replacements are refused first where one names a plugin the
	// configuration never names, or is no plugin reference.
	named := map[string]bool{}
	for _, p := range gen.Plugins {
		if p.Remote != "" {
			name, _ := bsrSplit(p.Remote)
			named[name] = true
		}
	}
	if err := unusedReplacements("plugin", repl.Plugins, named, bufconfig.GenFileName+" names no such plugin"); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(repl.Plugins) {
		if err := genfile.CheckReference(repl.Plugins[name]); err != nil {
			return nil, fmt.Errorf("--plugin %s=%s: %v", name, repl.Plugins[name], err)
		}
	}
	var facts []Fact
	report := func(f Fact) { facts = append(facts, f) }
	f := &genfile.File{}
	for i, p := range gen.Plugins {
		key := fmt.Sprintf("%s plugins[%d]", bufconfig.GenFileName, i)
		entry := genfile.Plugin{Out: p.Out, Opt: p.Opt}
		kept := true
		switch {
		case p.Remote != "":
			name, version := bsrSplit(p.Remote)
			ref, fact := pluginRef(key+".remote "+p.Remote, name, version, repl)
			report(fact)
			entry.Scheme, entry.Ref, kept = plugin.SchemeOCI, ref, ref != ""
		case len(p.Local) == 1:
			entry.Scheme, entry.Ref = plugin.SchemeLocal, p.Local[0]
			report(mapped(key+".local "+p.Local[0], "local: "+p.Local[0]))
		case len(p.Local) > 1:
			report(unmapped(key+".local "+strings.Join(p.Local, " "), "a command with arguments: pb runs an executable alone"))
			kept = false
		case p.ProtocBuiltin != "":
			report(unmapped(key+".protoc_builtin "+p.ProtocBuiltin, "pb runs no protoc"))
			kept = false
		default:
			// No form pb runs: v1's alpha remote key, reported below
			// among the keys passed over.
			kept = false
		}
		if kept {
			if err := genfile.CheckOut(p.Out); err != nil {
				report(unmapped(key+".out "+p.Out, "pb writes within the resolution root: "+err.Error()))
				kept = false
			} else {
				report(mapped(key+".out "+p.Out, "out: "+p.Out))
			}
		}
		if kept && p.Opt != "" {
			report(mapped(key+".opt "+p.Opt, "opt: "+p.Opt))
		}
		if kept {
			f.Plugins = append(f.Plugins, entry)
		}
		for _, u := range p.Unmodeled {
			k := string(u)
			if j := strings.LastIndexByte(k, '.'); j >= 0 {
				k = k[j+1:]
			}
			reason, known := keyReasons[k]
			if !known {
				reason = "a key the migration does not model"
			}
			report(unmapped(key+"."+k, reason))
		}
	}
	if gen.Inputs {
		report(unmapped(bufconfig.GenFileName+" inputs", "pb generates over the workspace's own files"))
	}
	if len(f.Plugins) == 0 {
		report(unmapped(bufconfig.GenFileName+" plugins", "no plugin mapped: no generation file is written"))
		// The managed mode with it: disabled or empty, what it would
		// have been; else read for nothing, with no file to hold it.
		switch m := gen.Managed; {
		case m == nil:
		case !m.Enabled || len(m.Overrides) == 0 && len(m.Forms) == 0:
			_, fs := managedOverrides(m)
			facts = append(facts, fs...)
		default:
			report(unmapped(bufconfig.GenFileName+" managed", "no generation file is written, no override with it"))
		}
		return facts, nil
	}
	overrides, fs := managedOverrides(gen.Managed)
	facts = append(facts, fs...)
	f.Overrides = overrides
	if _, err := genfile.Encode(f); err != nil {
		return nil, fmt.Errorf("the generation file: %w", err)
	}
	l.Gen = f
	return facts, nil
}

// bsrSplit splits a BSR name as buf spells it, `remote/owner/name`
// with a `:ref` after or none: the ref follows the last colon after
// the last slash, so a remote naming a port is no ref.
func bsrSplit(s string) (name, ref string) {
	slash := strings.LastIndexByte(s, '/')
	if colon := strings.LastIndexByte(s, ':'); colon > slash {
		return s[:colon], s[colon+1:]
	}
	return s, ""
}

// unusedReplacements refuses replacements naming what the
// configuration never declares, every such flag named in one error:
// a stale flag is the likelier fault, and a lookup it took part in
// would name it as if it counted.
func unusedReplacements[V any](flag string, repl map[string]V, declared map[string]bool, why string) error {
	var unused []string
	for _, name := range sortedKeys(repl) {
		if !declared[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		return fmt.Errorf("--%s %s: %s", flag, strings.Join(unused, ", --"+flag+" "), why)
	}
	return nil
}

// pluginRef is a BSR plugin's reference: the replacement's, or the
// table's repository at the version — the one named where the fork
// builds it, the highest built where none is named — with the fact
// either way; "" with an unmapped fact where neither holds.
func pluginRef(key, name, version string, repl Replacements) (string, Fact) {
	if ref, ok := repl.Plugins[name]; ok {
		return ref, mapped(key, "ref: "+ref+" (--plugin)")
	}
	e, ok := Plugins[name]
	if !ok {
		return "", unmapped(key, "no entry in the plugin table: pass --plugin "+name+"=<reference>")
	}
	if version == "" {
		v := e.versions[len(e.versions)-1]
		return e.repo + ":" + v, mapped(key, "ref: "+e.repo+":"+v+" (the plugin table; no version named, the highest the fork builds)")
	}
	for _, v := range e.versions {
		if v == version {
			return e.repo + ":" + v, mapped(key, "ref: "+e.repo+":"+v+" (the plugin table)")
		}
	}
	return "", unmapped(key, "the fork builds "+strings.Join(e.versions, ", ")+", not "+version+": pass --plugin "+name+"=<reference>")
}

// filesGlob is the generation file's glob for a managed path: the
// path as buf matches it — a file's path relative to its module,
// equal to it or under it — over the path and everything under it,
// `**` for the root; a path buf refuses, escaping or absolute, is
// refused too.
func filesGlob(spelled string) (string, error) {
	p, err := rootpath.Clean(spelled, "the module")
	if err != nil {
		return "", err
	}
	if p == "." {
		return "**", nil
	}
	return glob.Quote(p) + "/**", nil
}

// managedOverrides maps buf's managed mode to overrides
// (REQ-migrate-gen): each declarative entry an override over every
// file or over the files its path names, buf's own heuristics and
// what names a module unmapped facts.
func managedOverrides(m *bufconfig.Managed) ([]genfile.Override, []Fact) {
	if m == nil {
		return nil, nil
	}
	where := bufconfig.GenFileName + " managed"
	if !m.Enabled {
		return nil, []Fact{mapped(where+".enabled false", "nothing: managed mode is disabled, its entries read for nothing")}
	}
	var out []genfile.Override
	var facts []Fact
	report := func(f Fact) { facts = append(facts, f) }
	add := func(key, spec, option, value string) {
		out = append(out, genfile.Override{Files: spec, Option: option, Value: value})
		report(mapped(key, "overrides: files "+spec+" option "+option+" value "+value))
	}
	for i, o := range m.Overrides {
		key := fmt.Sprintf("%s.override[%d]", where, i)
		switch {
		case o.FieldOption != "":
			report(unmapped(key+" field_option="+o.FieldOption, "a field option: pb's overrides are file options"))
			continue
		case o.Module != "":
			report(unmapped(key+" file_option="+o.FileOption+" module="+o.Module, "pb's override files are module-relative globs, naming no module"))
			continue
		case computed[o.FileOption]:
			report(unmapped(key+" file_option="+o.FileOption+" value="+o.Value, "buf's own heuristic, a value computed per file from its package: pb declares values alone"))
			continue
		}
		spec, scope := "**", ""
		if o.Path != "" {
			g, err := filesGlob(o.Path)
			if err != nil {
				report(unmapped(key+" file_option="+o.FileOption+" path="+o.Path, "no path buf matches: "+err.Error()))
				continue
			}
			spec, scope = g, " path="+o.Path
		}
		add(key+" file_option="+o.FileOption+scope, spec, o.FileOption, o.Value)
	}
	for _, form := range m.Forms {
		key := where + "." + form.Option
		declared := form.Option == "optimize_for" || form.Option == "objc_class_prefix" || form.Option == "swift_prefix"
		if declared && form.Default != "" {
			add(key+".default "+form.Default, "**", form.Option, form.Default)
		} else if !declared {
			// A prefix, suffix or per-package form: buf computes each
			// file's value from its package; pb declares values alone.
			spelled := form.Default
			if spelled == "" {
				spelled = "(no default)"
			}
			report(unmapped(key+" "+spelled, "buf's own heuristic, a value computed per file from its package: pb declares values alone"))
		}
		for _, e := range form.Except {
			report(unmapped(key+".except "+e, "names a module, which a module-relative glob cannot"))
		}
		for _, mod := range sortedKeys(form.Override) {
			report(unmapped(key+".override "+mod+"="+form.Override[mod], "names a module, which a module-relative glob cannot"))
		}
	}
	for _, d := range m.Disables {
		report(unmapped(bufconfig.GenFileName+" "+d, "buf's own heuristic: pb declares values alone, disabling nothing"))
	}
	if len(m.Overrides) == 0 && len(m.Forms) == 0 {
		report(unmapped(where+".enabled true", "enabled with no explicit override: buf's own heuristic, pb declares values alone"))
	}
	return out, facts
}
