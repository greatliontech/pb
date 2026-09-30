package migrate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/rootpath"
)

// CatalogRegistry is the registry the plugin catalog publishes under
// (migrate.md, the plugin catalog).
const CatalogRegistry = "ghcr.io/greatliontech/pb-plugins"

// Catalog is the plugin catalog (migrate.md, the mapping table term):
// buf's plugin names the catalog repository publishes, sorted. The
// spec's list is the same, held to this one by
// TestPluginCatalogMatchesSpec; the repository's catalog and the
// registry are held to it by TestPluginCatalog, live.
var Catalog = []string{
	"buf.build/bufbuild/es",
	"buf.build/connectrpc/es",
	"buf.build/connectrpc/go",
	"buf.build/grpc/csharp",
	"buf.build/grpc/go",
	"buf.build/grpc/web",
	"buf.build/protocolbuffers/csharp",
	"buf.build/protocolbuffers/go",
	"buf.build/protocolbuffers/js",
}

// CatalogRepository is the rename rule: `buf.build/<owner>/<plugin>`
// is published at `<CatalogRegistry>/<owner>/<plugin>`.
func CatalogRepository(name string) string {
	return CatalogRegistry + strings.TrimPrefix(name, "buf.build")
}

func inCatalog(name string) bool {
	i := sort.SearchStrings(Catalog, name)
	return i < len(Catalog) && Catalog[i] == name
}

// TagLister lists a repository's tags at its registry.
type TagLister func(ctx context.Context, repository string) ([]string, error)

// versionTag is a tag spelled as a version: `v` and dot-separated
// decimal numbers with no leading zero, buf's spelling of a plugin's
// version (REQ-migrate-gen).
var versionTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))+$`)

// highestVersionTag is the highest version tag among tags, by number
// component by component, a shorter tag equal so far the lesser;
// none where no tag is a version. Two version tags never compare
// equal: the grammar admits one spelling per number.
func highestVersionTag(tags []string) string {
	best := ""
	for _, t := range tags {
		if versionTag.MatchString(t) && (best == "" || tagLess(best, t)) {
			best = t
		}
	}
	return best
}

// tagLess orders version tags by number, component by component, a
// shorter tag equal so far the lesser. A component is compared as
// its decimal digits — the shorter run of digits the lesser, equal
// runs by their text — so no number is too large to order.
func tagLess(a, b string) bool {
	as, bs := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if len(as[i]) != len(bs[i]) {
			return len(as[i]) < len(bs[i])
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// keyReasons are the reasons a plugin entry's passed-over keys map to
// nothing (REQ-migrate-gen), by key.
var keyReasons = map[string]string{
	"strategy":      "pb generates over the workspace's own files under one strategy",
	"include_wkt":   "pb generates for no well-known file: the toolchain's copy is never a target",
	"types":         "pb generates over the workspace's own files under one strategy",
	"exclude_types": "pb generates over the workspace's own files under one strategy",
	"revision":      "pb pins a plugin by its image digest, not a build revision",
	"protoc_path":   "pb runs no protoc",
	"remote":        "buf's alpha remote plugin, run by the BSR: pb runs every plugin itself",
}

// computed are buf's managed-mode file options that are no protobuf
// option but a rule buf computes each file's value by: a prefix or
// suffix over the package.
var computed = map[string]bool{
	"go_package_prefix": true, "java_package_prefix": true, "java_package_suffix": true,
	"csharp_namespace_prefix": true, "php_metadata_namespace_suffix": true, "ruby_package_suffix": true,
}

// Template is a generation template beside the configuration, read
// as a `buf.gen.yaml` is (REQ-migrate-gen).
type Template struct {
	Name string
	Gen  *bufconfig.Gen
}

// StatFunc reports whether a root-relative path exists in the tree
// and whether it is a directory.
type StatFunc func(rel string) (exists, isDir bool)

// Gen builds the generation file from buf.gen.yaml and the templates
// beside it (REQ-migrate-gen), the templates' entries after the
// file's, each file's inputs and managed mode its own entries'.
// Each plugin entry naming a BSR plugin is looked up among the
// replacements and then in the plugin catalog, a name either holds
// becoming a `ref` entry: the replacement's reference as given, or
// the catalog's repository for the name at the version named as buf
// spells it (one no tag can spell an unmapped fact), at the highest
// version tag the registry lists where none is named — listed once
// per name through tags, nil for no registry access; a name neither
// holds, or a listing that fails or holds no version tag, an
// unmapped fact naming the flag's form. A local entry becomes a
// `local` entry of its command and arguments, an unmapped fact where
// pb's schema refuses the command; a protoc builtin an unmapped
// fact; out as written where pb's schema takes it, an unmapped fact
// where not; opt as joined; include_imports as itself; each key the
// reader passed over an unmapped fact naming it. A file's directory
// inputs' paths become the file's entries' files patterns
// (inputPatterns), an input of another kind or an exclusion an
// unmapped fact. buf's managed mode becomes overrides where an entry
// is declarative — a file option with a value over every file or
// over the files a path names, the path relative to a file's module
// as buf matches it and pb's globs are; v1's boolean options, its
// `override` map of option to file to value, and the defaults of
// `optimize_for`, `objc_class_prefix` and `swift_prefix` — and an
// unmapped fact where it is buf's own heuristic or names a module,
// which a module-relative glob cannot; a template's managed mode is
// the file's own where it maps to the same overrides, an unmapped
// fact where not, pb's overrides being one set over every entry, as
// its clean is one for every output directory: the first file's,
// a later file disagreeing an unmapped fact. The file is set on the
// layout, nil where no plugin mapped, the managed mode then read for
// nothing, and the facts returned.
func Gen(ctx context.Context, gen *bufconfig.Gen, templates []Template, repl Replacements, l *Layout, tags TagLister, stat StatFunc) ([]Fact, error) {
	if (gen == nil && len(templates) == 0) || l == nil {
		return nil, fmt.Errorf("no buf.gen.yaml read")
	}
	type genFile struct {
		name string
		gen  *bufconfig.Gen
	}
	var files []genFile
	if gen != nil {
		files = append(files, genFile{bufconfig.GenFileName, gen})
	}
	for _, t := range templates {
		files = append(files, genFile{t.Name, t.Gen})
	}
	// The replacements are refused first where one names a plugin no
	// file names, or is no plugin reference.
	named := map[string]bool{}
	for _, fl := range files {
		for _, p := range fl.gen.Plugins {
			if p.Remote != "" {
				name, _ := bsrSplit(p.Remote)
				named[name] = true
			}
		}
	}
	if err := unusedReplacements("plugin", repl.Plugins, named, "no generation file names such a plugin"); err != nil {
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
	listed := map[string]listing{}
	first := files[0]
	for fi, fl := range files {
		// The inputs' facts follow the file's entries, as buf's keys
		// are read: the patterns first, their facts held back.
		var inputFacts []Fact
		patterns, restricted, err := inputPatterns(fl.name, fl.gen, l, stat, func(f Fact) { inputFacts = append(inputFacts, f) })
		if err != nil {
			return nil, err
		}
		for i, p := range fl.gen.Plugins {
			key := fmt.Sprintf("%s plugins[%d]", fl.name, i)
			entry := genfile.Plugin{Out: p.Out, Opt: p.Opt}
			kept := true
			switch {
			case p.Remote != "":
				name, version := bsrSplit(p.Remote)
				ref, fact := pluginRef(ctx, key+".remote "+p.Remote, name, version, repl, tags, listed)
				report(fact)
				entry.Scheme, entry.Ref, kept = plugin.SchemeOCI, ref, ref != ""
			case len(p.Local) > 0:
				// The command as written where pb's schema takes it, as
				// out below; buf takes any non-empty text.
				if err := genfile.CheckLocal(p.Local[0]); err != nil {
					report(unmapped(key+".local "+strings.Join(p.Local, " "), "pb's schema refuses the command: "+err.Error()))
					kept = false
					break
				}
				entry.Scheme, entry.Ref = plugin.SchemeLocal, p.Local[0]
				if len(p.Local) > 1 {
					entry.Args = p.Local[1:]
				}
				report(mapped(key+".local "+strings.Join(p.Local, " "), "local: "+strings.Join(p.Local, " ")))
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
			if kept && p.IncludeImports {
				entry.IncludeImports = true
				report(mapped(key+".include_imports true", "include_imports: true"))
			}
			if kept && restricted {
				entry.Files = append([]string(nil), patterns...)
			}
			if kept {
				f.Plugins = append(f.Plugins, entry)
			}
			reportKeys(report, key, p.Unmodeled)
		}
		facts = append(facts, inputFacts...)
		// One clean for every output directory: the first file's.
		switch {
		case fi == 0 && fl.gen.Clean:
			f.Clean = true
			report(mapped(fl.name+" clean true", "clean: true"))
		case fi > 0 && fl.gen.Clean != first.gen.Clean && len(fl.gen.Plugins) > 0:
			report(unmapped(fl.name+" clean", "differs from "+first.name+"'s: pb empties every entry's output directory or none"))
		}
		// One set of overrides over every entry: the first file's
		// managed mode. A template's managed mode is read whole, its
		// facts its own, and is the first file's where it gives the
		// same overrides — none where the first gives none; a
		// template giving other overrides, or none where the first
		// gives some, is unmapped: its entries run under the first's.
		if fi > 0 {
			ov, fs := managedOverrides(fl.name, fl.gen.Managed)
			base, _ := managedOverrides(first.name, first.gen.Managed)
			facts = append(facts, fs...)
			switch {
			case slices.Equal(ov, base) && fl.gen.Managed != nil:
				report(mapped(fl.name+" managed", "the overrides "+first.name+"'s managed mode gives"))
			case slices.Equal(ov, base):
			case fl.gen.Managed == nil:
				report(unmapped(fl.name+" managed", "none, while "+first.name+"'s gives overrides: pb's overrides are one set over every entry"))
			default:
				report(unmapped(fl.name+" managed", "differs from "+first.name+"'s: pb's overrides are one set over every entry"))
			}
		}
	}
	if len(f.Plugins) == 0 {
		report(unmapped(first.name+" plugins", "no plugin mapped: no generation file is written"))
		// The managed mode with it: disabled or empty, what it would
		// have been; else read for nothing, with no file to hold it.
		switch m := first.gen.Managed; {
		case m == nil:
		case !m.Enabled || len(m.Overrides) == 0 && len(m.Forms) == 0:
			_, fs := managedOverrides(first.name, m)
			facts = append(facts, fs...)
		default:
			report(unmapped(first.name+" managed", "no generation file is written, no override with it"))
		}
		return facts, nil
	}
	overrides, fs := managedOverrides(first.name, first.gen.Managed)
	facts = append(facts, fs...)
	f.Overrides = overrides
	if _, err := genfile.Encode(f); err != nil {
		return nil, fmt.Errorf("the generation file: %w", err)
	}
	l.Gen = f
	return facts, nil
}

// reportKeys reports the keys the reader passed over under an entry,
// each an unmapped fact under key naming the key's own reason where
// keyReasons has one.
func reportKeys(report func(Fact), key string, keys []bufconfig.Unmodeled) {
	for _, u := range keys {
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

// inputPatterns maps a file's inputs to its entries' files patterns
// (REQ-migrate-gen): each directory input's paths, root-relative and
// within the input's directory, a path under one workspace module
// becoming that module-relative path's pattern — the file's own, the
// directory's and everything under it — a mapped fact naming it; a
// path under no module, one that is a module's directory among
// several, one that exists nowhere, or one whose module-relative path
// exists under another module too an unmapped fact; a directory
// input naming no paths read as a path of its own, the root every
// workspace file; an exclusion, an input of another kind, or a
// directory outside the root, an unmapped fact. restricted is false
// where an input takes every workspace file, or where no input maps
// to a pattern.
func inputPatterns(file string, gen *bufconfig.Gen, l *Layout, stat StatFunc, report func(Fact)) ([]string, bool, error) {
	var patterns []string
	seen := map[string]bool{}
	restricted := gen.Inputs != nil
	dirs := sortedKeys(l.Modules)
	for i, in := range gen.Inputs {
		key := fmt.Sprintf("%s inputs[%d]", file, i)
		if in.Kind != "directory" {
			report(unmapped(key+"."+in.Kind+" "+in.Value, "an input pb reads nothing from: pb generates over the workspace's own files"))
			continue
		}
		base, err := rootpath.Clean(in.Value, "the resolution root")
		if err != nil {
			// buf reads a directory anywhere; pb reads the root's
			// modules alone.
			report(unmapped(key+".directory "+in.Value, "outside the resolution root: pb generates over the workspace's own files"))
			continue
		}
		// A directory naming no paths is read as a path of its own:
		// the root is every workspace file, a module or a directory
		// within one is what a path naming it is.
		paths := in.Paths
		keys := make([]string, len(paths))
		for j, p := range paths {
			keys[j] = fmt.Sprintf("%s.paths[%d] %s", key, j, p)
		}
		if len(paths) == 0 {
			if base == "." {
				restricted = false
				report(mapped(key+".directory "+in.Value, "every workspace file: pb generates over the modules' own files"))
				continue
			}
			paths, keys = []string{base}, []string{key + ".directory " + in.Value}
		}
		for j, p := range paths {
			pkey := keys[j]
			rel, err := rootpath.Clean(p, "the resolution root")
			if err != nil {
				return nil, false, fmt.Errorf("%w: %s: %v", bufconfig.ErrInvalid, pkey, err)
			}
			if base != "." && rel != base && !rootpath.Contains(base, rel) {
				return nil, false, fmt.Errorf("%w: %s lies outside the input directory %s", bufconfig.ErrInvalid, pkey, base)
			}
			module := ""
			for _, d := range dirs {
				if d == rel || rootpath.Contains(d, rel) {
					module = d
				}
			}
			switch {
			case module == "":
				report(unmapped(pkey, "under no workspace module: pb generates over the modules' own files"))
				continue
			case module == rel && len(dirs) > 1:
				report(unmapped(pkey, "a whole module among several: pb's patterns are module-relative and name every module"))
				continue
			case module == rel:
				report(mapped(pkey, "every file of the module"))
				restricted = false
				continue
			}
			modRel := rel
			if module != "." {
				modRel = strings.TrimPrefix(rel, module+"/")
			}
			exists, isDir := stat(rel)
			if !exists {
				report(unmapped(pkey, "no such path"))
				continue
			}
			var other string
			for _, d := range dirs {
				if d != module {
					if ex, _ := stat(path.Join(d, modRel)); ex {
						other = path.Join(d, modRel)
					}
				}
			}
			if other != "" {
				report(unmapped(pkey, "the module-relative path "+modRel+" lies in "+other+" too: a pattern would name both"))
				continue
			}
			pattern := glob.Quote(modRel)
			if isDir {
				pattern += "/**"
			}
			if !seen[pattern] {
				seen[pattern] = true
				patterns = append(patterns, pattern)
			}
			report(mapped(pkey, "files: "+pattern))
		}
		for j, p := range in.ExcludePaths {
			report(unmapped(fmt.Sprintf("%s.exclude_paths[%d] %s", key, j, p), "pb's patterns name what to generate for, excluding nothing"))
		}
		reportKeys(report, key, in.Unmodeled)
	}
	if len(patterns) == 0 {
		restricted = false
	}
	return patterns, restricted, nil
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
// catalog's repository at the version — the one named as spelled, the
// highest tag the registry lists where none is named, listed once
// per repository through listed — with the fact either way; "" with
// an unmapped fact where neither holds or the listing gives no
// version.
func pluginRef(ctx context.Context, key, name, version string, repl Replacements, tags TagLister, listed map[string]listing) (string, Fact) {
	if ref, ok := repl.Plugins[name]; ok {
		return ref, mapped(key, "ref: "+ref+" (--plugin)")
	}
	if !inCatalog(name) {
		return "", unmapped(key, "not in the plugin catalog: pass --plugin "+name+"=<reference>")
	}
	repo := CatalogRepository(name)
	if version != "" {
		// buf accepts a version no tag can spell (a build suffix's
		// `+`): such an entry is unmapped, never a file pb refuses.
		if err := genfile.CheckReference(repo + ":" + version); err != nil {
			return "", unmapped(key, "no tag spells the version: "+err.Error()+": pass --plugin "+name+"=<reference>")
		}
		return repo + ":" + version, mapped(key, "ref: "+repo+":"+version+" (the catalog)")
	}
	l, ok := listed[repo]
	if !ok {
		l = listTags(ctx, tags, repo)
		listed[repo] = l
	}
	switch {
	case l.err != nil:
		return "", unmapped(key, "listing the catalog's tags failed: "+l.err.Error()+": name a version or pass --plugin "+name+"=<reference>")
	case l.highest == "":
		return "", unmapped(key, "the catalog publishes no version of it yet: pass --plugin "+name+"=<reference>")
	}
	return repo + ":" + l.highest, mapped(key, "ref: "+repo+":"+l.highest+" (the catalog; no version named, the highest tag published)")
}

// listing is what one listing of a repository's tags gave: the
// highest version tag, or why none.
type listing struct {
	highest string
	err     error
}

// listTags lists a repository's version tags once (REQ-migrate-gen), no
// lister being no registry access.
func listTags(ctx context.Context, tags TagLister, repo string) listing {
	if tags == nil {
		return listing{err: errors.New("no registry access")}
	}
	all, err := tags(ctx, repo)
	if err != nil {
		return listing{err: err}
	}
	return listing{highest: highestVersionTag(all)}
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
func managedOverrides(file string, m *bufconfig.Managed) ([]genfile.Override, []Fact) {
	if m == nil {
		return nil, nil
	}
	where := file + " managed"
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
		report(unmapped(file+" "+d, "buf's own heuristic: pb declares values alone, disabling nothing"))
	}
	if len(m.Overrides) == 0 && len(m.Forms) == 0 {
		report(unmapped(where+".enabled true", "enabled with no explicit override: buf's own heuristic, pb declares values alone"))
	}
	return out, facts
}
