package migrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// fact; out as written
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
func Gen(ctx context.Context, gen *bufconfig.Gen, repl Replacements, l *Layout, tags TagLister) ([]Fact, error) {
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
	listed := map[string]listing{}
	for i, p := range gen.Plugins {
		key := fmt.Sprintf("%s plugins[%d]", bufconfig.GenFileName, i)
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
