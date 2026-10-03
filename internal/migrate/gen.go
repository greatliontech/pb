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
	"buf.build/apple/swift",
	"buf.build/bufbuild/connect-es",
	"buf.build/bufbuild/connect-go",
	"buf.build/bufbuild/connect-query",
	"buf.build/bufbuild/connect-swift",
	"buf.build/bufbuild/connect-swift-mocks",
	"buf.build/bufbuild/connect-web",
	"buf.build/bufbuild/es",
	"buf.build/bufbuild/protoschema-bigquery",
	"buf.build/bufbuild/protoschema-jsonschema",
	"buf.build/bufbuild/protoschema-pubsub",
	"buf.build/bufbuild/validate-cpp",
	"buf.build/bufbuild/validate-go",
	"buf.build/community/chrusty-jsonschema",
	"buf.build/community/google-gnostic-openapi",
	"buf.build/community/mercari-grpc-federation",
	"buf.build/community/mfridman-go-json",
	"buf.build/community/mitchellh-go-json",
	"buf.build/community/planetscale-vtprotobuf",
	"buf.build/community/pseudomuto-doc",
	"buf.build/community/roadrunner-server-php-grpc",
	"buf.build/community/scalapb-scala",
	"buf.build/community/sudorandom-connect-openapi",
	"buf.build/community/timostamm-protobuf-ts",
	"buf.build/connectrpc/dart",
	"buf.build/connectrpc/es",
	"buf.build/connectrpc/go",
	"buf.build/connectrpc/gosimple",
	"buf.build/connectrpc/query-es",
	"buf.build/connectrpc/rust",
	"buf.build/connectrpc/swift",
	"buf.build/connectrpc/swift-mocks",
	"buf.build/grpc-ecosystem/gateway",
	"buf.build/grpc-ecosystem/openapiv2",
	"buf.build/grpc-ecosystem/openapiv3",
	"buf.build/grpc/cpp",
	"buf.build/grpc/csharp",
	"buf.build/grpc/go",
	"buf.build/grpc/java",
	"buf.build/grpc/node",
	"buf.build/grpc/objc",
	"buf.build/grpc/php",
	"buf.build/grpc/python",
	"buf.build/grpc/ruby",
	"buf.build/grpc/swift",
	"buf.build/grpc/swift-protobuf",
	"buf.build/grpc/web",
	"buf.build/pluginrpc/go",
	"buf.build/protocolbuffers/cpp",
	"buf.build/protocolbuffers/csharp",
	"buf.build/protocolbuffers/dart",
	"buf.build/protocolbuffers/go",
	"buf.build/protocolbuffers/java",
	"buf.build/protocolbuffers/js",
	"buf.build/protocolbuffers/kotlin",
	"buf.build/protocolbuffers/objc",
	"buf.build/protocolbuffers/php",
	"buf.build/protocolbuffers/pyi",
	"buf.build/protocolbuffers/python",
	"buf.build/protocolbuffers/ruby",
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
	"types":         "pb generates over the workspace's own files under one strategy",
	"exclude_types": "pb generates over the workspace's own files under one strategy",
	"revision":      "pb pins a plugin by its image digest, not a build revision",
	"protoc_path":   "pb runs no protoc",
	"remote":        "buf's alpha remote plugin, run by the BSR: pb runs every plugin itself",
}

// derivation reads buf's managed-mode prefix and suffix options —
// the option's name and `_prefix` or `_suffix` — as the option each
// derives and its axis, through the rules pb's derived overrides
// admit (generation.md REQ-gen-overrides-derived): what pb would
// refuse is no derivation, `objc_class_prefix` and `swift_prefix`
// being values.
func derivation(name string) (option string, prefix, ok bool) {
	if o, found := strings.CutSuffix(name, "_prefix"); found && genfile.CheckDerivation(genfile.Override{Option: o, Prefix: "x"}) == nil {
		return o, true, true
	}
	if o, found := strings.CutSuffix(name, "_suffix"); found && genfile.CheckDerivation(genfile.Override{Option: o, Suffix: "x"}) == nil {
		return o, false, true
	}
	return "", false, false
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
	// One clean for every output directory where every file with
	// entries agrees; where they differ, each cleaning file's entries
	// carry their own (generation.md REQ-gen-clean).
	anyClean, allClean := false, true
	for _, fl := range files {
		if len(fl.gen.Plugins) > 0 {
			anyClean, allClean = anyClean || fl.gen.Clean, allClean && fl.gen.Clean
		}
	}
	rel := l.Rel
	for _, fl := range files {
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
				// The directory cleaned as buf cleans it and read
				// relative to the root through the configuration's
				// directory: within the root, pb's; escaping it, nowhere
				// pb writes.
				out := path.Join(rel, p.Out)
				if path.IsAbs(p.Out) || out == ".." || strings.HasPrefix(out, "../") {
					report(unmapped(key+".out "+p.Out, "pb writes within the resolution root: "+p.Out+" escapes it"))
					kept = false
				} else if err := genfile.CheckOut(out); err != nil {
					report(unmapped(key+".out "+p.Out, "pb writes within the resolution root: "+err.Error()))
					kept = false
				} else {
					entry.Out = out
					report(mapped(key+".out "+p.Out, "out: "+out))
				}
			}
			if kept && p.Opt != "" {
				report(mapped(key+".opt "+p.Opt, "opt: "+p.Opt))
			}
			if kept && p.IncludeImports {
				entry.IncludeImports = true
				report(mapped(key+".include_imports true", "include_imports: true"))
			}
			if kept && p.IncludeWKT {
				entry.IncludeWKT = true
				report(mapped(key+".include_wkt true", "include_wkt: true"))
			}
			if kept && fl.gen.Clean && !allClean {
				entry.Clean = true
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
		switch {
		case fl.gen.Clean && len(fl.gen.Plugins) == 0:
			report(mapped(fl.name+" clean true", "nothing: the file has no entry to clean for"))
		case fl.gen.Clean && allClean && fl.name == first.name:
			f.Clean = true
			report(mapped(fl.name+" clean true", "clean: true"))
		case fl.gen.Clean && allClean:
			report(mapped(fl.name+" clean true", "clean: true, "+first.name+"'s, every file agreeing"))
		case fl.gen.Clean:
			report(mapped(fl.name+" clean true", "clean: true on each of its entries: the files differ"))
		case anyClean && len(fl.gen.Plugins) > 0:
			report(mapped(fl.name+" clean false", "the file's entries clean nothing: the files differ"))
		}
		// One set of overrides over every entry: the first file's
		// managed mode. A template's managed mode is read whole, its
		// facts its own, and is the first file's where it gives the
		// same overrides — none where the first gives none; a
		// template giving other overrides, or none where the first
		// gives some, is unmapped: its entries run under the first's.
		if fl.name != first.name {
			ov, fs := managedOverrides(fl.name, fl.gen.Managed, l)
			base, _ := managedOverrides(first.name, first.gen.Managed, l)
			facts = append(facts, fs...)
			switch {
			case slices.EqualFunc(ov, base, genfile.Override.Equal) && fl.gen.Managed != nil:
				report(mapped(fl.name+" managed", "the overrides "+first.name+"'s managed mode gives"))
			case slices.EqualFunc(ov, base, genfile.Override.Equal):
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
		case !m.Enabled:
			_, fs := managedOverrides(first.name, m, l)
			facts = append(facts, fs...)
		default:
			report(unmapped(first.name+" managed", "no generation file is written, no override with it"))
		}
		return facts, nil
	}
	overrides, fs := managedOverrides(first.name, first.gen.Managed, l)
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
	rel := l.Rel
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
		rooted := func(p string) string { return path.Join(rel, p) }
		// A directory naming no paths is read as a path of its own:
		// the root is every workspace file, a module or a directory
		// within one is what a path naming it is.
		paths := in.Paths
		keys := make([]string, len(paths))
		for j, p := range paths {
			keys[j] = fmt.Sprintf("%s.paths[%d] %s", key, j, p)
		}
		if len(paths) == 0 {
			if rooted(base) == "." {
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
			rel = rooted(rel)
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

// scopePath is a managed path as buf matches it — a file's path
// relative to its module, equal to it or under it — cleaned, "." for
// the root; a path buf refuses, escaping or absolute, is refused too.
func scopePath(spelled string) (string, error) {
	return rootpath.Clean(spelled, "the module")
}

// scopeGlob is the generation file's glob over a path: the path and
// everything under it, `**` for the root.
func scopeGlob(p string) string {
	if p == "." {
		return "**"
	}
	return glob.Quote(p) + "/**"
}

// scope is where a managed rule applies: the one module it names,
// "" for every module, and a path within the module, "." for the
// whole of it (generation.md REQ-gen-schema's module and files).
type scope struct{ module, path string }

// contains reports whether s holds every file of t: a module holds
// its own, "" every module; managed paths nest or lie apart.
func (s scope) contains(t scope) bool {
	return (s.module == "" || s.module == t.module) && (s.path == t.path || rootpath.Contains(s.path, t.path))
}

// meet is the files both scopes cover — the narrower path where they
// nest, under the module either names — and false where they lie
// apart.
func (s scope) meet(t scope) (scope, bool) {
	m := s.module
	if m == "" {
		m = t.module
	} else if t.module != "" && t.module != m {
		return scope{}, false
	}
	switch {
	case s.path == t.path || rootpath.Contains(s.path, t.path):
		return scope{m, t.path}, true
	case rootpath.Contains(t.path, s.path):
		return scope{m, s.path}, true
	}
	return scope{}, false
}

// override is an override of option over the scope, its value or
// axes unset.
func (s scope) override(option string) genfile.Override {
	return genfile.Override{Files: scopeGlob(s.path), Module: s.module, Option: option}
}

// managedOverride is one override the managed mode gave, with the
// scope it covers and the buf key that wrote it.
type managedOverride struct {
	o     genfile.Override
	scope scope
	key   string
}

// managedRule is one rule buf's managed mode reads, in buf's order:
// buf's default for an option, a form's or an override's value, a
// prefix or suffix rule, or a derivation from the file alone, over a
// scope, with the buf key that wrote it.
type managedRule struct {
	key    string
	sc     scope
	option string // the option; for a prefix or suffix rule, the axis name buf spells (`<option>_prefix`)
	value  string
	kind   ruleKind
	note   string // what the fact says beyond the override
}

type ruleKind int

const (
	ruleValue  ruleKind = iota // a value for the option
	ruleDerive                 // a prefix or suffix, option naming the axis
	ruleBare                   // a derivation from the file alone, buf's computed default
)

// label names the rule in another fact: its key and the option it
// sets, a default's key naming nine rules.
func (r managedRule) label() string { return r.key + " for " + r.baseOption() }

// baseOption is the option a rule sets, an axis name's base.
func (r managedRule) baseOption() string {
	if r.kind == ruleDerive {
		o, _, _ := derivation(r.option)
		return o
	}
	return r.option
}

// managedDefaults are buf's defaults under managed mode
// (REQ-migrate-gen): each option's rule, in buf's order, over every
// file.
var managedDefaults = []managedRule{
	{option: "cc_enable_arenas", value: "true", kind: ruleValue},
	{option: "csharp_namespace", kind: ruleBare},
	{option: "java_multiple_files", value: "true", kind: ruleValue},
	{option: "java_outer_classname", kind: ruleBare},
	{option: "java_package_prefix", value: "com", kind: ruleDerive},
	{option: "objc_class_prefix", kind: ruleBare},
	{option: "php_metadata_namespace", kind: ruleBare},
	{option: "php_namespace", kind: ruleBare},
	{option: "ruby_package", kind: ruleBare},
}

// managedDisable is a disable buf judges before any rule
// (REQ-migrate-gen): the option it names — an option, its prefix or
// suffix axis, or every option — and the module it scopes to, every
// module where "".
type managedDisable struct {
	key    string
	option string // "" for every option
	module string // "" for every module
}

// drops reports whether the disable removes the rule from what a file
// of its module sees: every rule, the option's rules whole, or the
// rules of the one axis.
func (d managedDisable) drops(r managedRule) bool {
	switch {
	case d.option == "":
		return true
	case r.kind == ruleDerive && r.option == d.option:
		return true // the axis named
	}
	// An axis name equals no rule's option; an option name drops its
	// values, derivations and default alike.
	return r.baseOption() == d.option
}

// managedOverrides maps buf's managed mode to overrides
// (REQ-migrate-gen) exactly as buf reads it per file: the rules —
// buf's defaults first, then each declarative entry, over every file
// or the files its path names, scoped to the module a rule names —
// are run once for every module under the disables naming none, and
// once more for each module a disable names, under that module's
// disables too, that module's overrides of the options affected
// scoped to it and every module's overrides of those options
// excepting it. A disable naming a path or a field option, and a rule
// naming a module no path stands for, are unmapped facts. A BSR
// module name is resolved through the layout's names and dependency
// paths.
func managedOverrides(file string, m *bufconfig.Managed, l *Layout) ([]genfile.Override, []Fact) {
	if m == nil {
		return nil, nil
	}
	where := file + " managed"
	if !m.Enabled {
		return nil, []Fact{mapped(where+".enabled false", "nothing: managed mode is disabled, its entries read for nothing")}
	}
	var facts []Fact
	report := func(f Fact) { facts = append(facts, f) }
	modulePath := func(name string) (string, bool) {
		if p, ok := l.Names[name]; ok {
			return p, true
		}
		p, ok := l.DepPaths[name]
		return p, ok
	}
	// A rule naming a module scopes to the module's path, resolved
	// through the layout; a name no module bears is unmapped.
	resolve := func(key, name string) (string, bool) {
		p, ok := modulePath(name)
		if !ok {
			report(unmapped(key, "names the module "+name+", which the configuration declares nowhere: no module path stands for it"))
		}
		return p, ok
	}
	// The rules, in buf's order: the defaults, then v1's forms (its
	// defaults; buf reads its booleans, then the forms, then the
	// per-file map, whatever the document's order — forms and
	// booleans name no option in common, so the forms go first here
	// and the overrides, booleans then the map in buf's order, after;
	// a form's except disables the form's option whole for the module,
	// as buf reads it, and its override per module is a rule scoped to
	// the module), then v2's overrides.
	var rules []managedRule
	for _, d := range managedDefaults {
		d.key, d.sc, d.note = where+".enabled true", scope{"", "."}, " (buf's default)"
		rules = append(rules, d)
	}
	var disables []managedDisable
	for _, form := range m.Forms {
		key := where + "." + form.Option
		_, _, derived := derivation(form.Option)
		kind := ruleValue
		if derived {
			kind = ruleDerive
		}
		if form.Default != "" {
			rules = append(rules, managedRule{key: key + ".default " + form.Default, sc: scope{"", "."}, option: form.Option, value: form.Default, kind: kind})
		}
		option := form.Option
		if o, _, isAxis := derivation(form.Option); isAxis {
			option = o
		}
		for _, e := range form.Except {
			if p, ok := resolve(key+".except "+e, e); ok {
				disables = append(disables, managedDisable{key + ".except " + e, option, p})
			}
		}
		for _, mod := range sortedKeys(form.Override) {
			k := key + ".override " + mod + "=" + form.Override[mod]
			if p, ok := resolve(k, mod); ok {
				rules = append(rules, managedRule{key: k, sc: scope{p, "."}, option: form.Option, value: form.Override[mod], kind: kind})
			}
		}
	}
	for i, o := range m.Overrides {
		key := fmt.Sprintf("%s.override[%d]", where, i)
		if o.FieldOption != "" {
			report(unmapped(key+" field_option="+o.FieldOption, "a field option: pb's overrides are file options"))
			continue
		}
		spelled, sc := "", scope{"", "."}
		if o.Module != "" {
			spelled = " module=" + o.Module
			p, ok := resolve(key+" file_option="+o.FileOption+spelled, o.Module)
			if !ok {
				continue
			}
			sc.module = p
		}
		if o.Path != "" {
			p, err := scopePath(o.Path)
			if err != nil {
				report(unmapped(key+" file_option="+o.FileOption+spelled+" path="+o.Path, "no path buf matches: "+err.Error()))
				continue
			}
			sc.path, spelled = p, spelled+" path="+o.Path
		}
		if _, _, isDerivation := derivation(o.FileOption); isDerivation {
			rules = append(rules, managedRule{key: key + " file_option=" + o.FileOption + " value=" + o.Value + spelled, sc: sc, option: o.FileOption, value: o.Value, kind: ruleDerive})
			continue
		}
		rules = append(rules, managedRule{key: key + " file_option=" + o.FileOption + spelled, sc: sc, option: o.FileOption, value: o.Value, kind: ruleValue})
	}
	for _, d := range m.Disables {
		key := file + " " + d.Spelled
		switch {
		case d.FieldOption != "" || d.Field != "":
			report(unmapped(key, "a field option: pb's overrides are file options"))
			continue
		case d.Path != "":
			report(unmapped(key, "disables over a path: pb's overrides name what they set, excluding no path"))
			continue
		}
		md := managedDisable{key: key, option: d.FileOption}
		if d.Module != "" {
			p, ok := resolve(key, d.Module)
			if !ok {
				continue
			}
			md.module = p
		}
		disables = append(disables, md)
	}
	// The classes: every module under the disables naming none, and
	// each module a disable names under its own too.
	var generic []managedDisable
	byModule := map[string][]managedDisable{}
	for _, d := range disables {
		if d.module == "" {
			generic = append(generic, d)
		} else {
			byModule[d.module] = append(byModule[d.module], d)
		}
	}
	admitted := func(r managedRule, ds []managedDisable) bool {
		for _, d := range ds {
			if d.drops(r) {
				return false
			}
		}
		return true
	}
	// Each disable's fact names the rules it drops.
	for _, d := range disables {
		var dropped []string
		for _, r := range rules {
			if d.drops(r) && (d.module == "" || r.sc.module == "" || r.sc.module == d.module) {
				dropped = append(dropped, r.label())
			}
		}
		what := "every option"
		if d.option != "" {
			what = d.option
		}
		switch {
		case len(dropped) == 0:
			report(mapped(d.key, "nothing: no rule sets "+what+" there"))
		case d.module == "":
			report(mapped(d.key, "nothing sets "+what+" beyond the rules left: "+strings.Join(dropped, ", ")+" dropped for every file"))
		default:
			report(mapped(d.key, "the module "+d.module+" sees "+what+" without "+strings.Join(dropped, ", ")+": its overrides of the option recomputed, every module's excepting it"))
		}
	}
	var genericRules []managedRule
	for _, r := range rules {
		if admitted(r, generic) {
			genericRules = append(genericRules, r)
		}
	}
	out, fs := runManagedRules(genericRules, "")
	facts = append(facts, fs...)
	for _, mod := range sortedKeys(byModule) {
		ds := byModule[mod]
		// The options the module's disables touch: every option, or
		// the base of each named.
		all := false
		affected := map[string]bool{}
		for _, d := range ds {
			if d.option == "" {
				all = true
				continue
			}
			o, _, isAxis := derivation(d.option)
			if !isAxis {
				o = d.option
			}
			affected[o] = true
		}
		var own []managedRule
		for _, r := range genericRules {
			if !admitted(r, ds) || r.sc.module != "" && r.sc.module != mod || !all && !affected[r.baseOption()] {
				continue
			}
			r.sc.module = mod
			own = append(own, r)
		}
		modOut, fs := runManagedRules(own, mod)
		facts = append(facts, fs...)
		// Every module's overrides of the affected options leave the
		// module out; one scoped to it is the module's own, replaced.
		var kept []managedOverride
		for _, o := range out {
			if !all && !affected[o.o.Option] {
				kept = append(kept, o)
				continue
			}
			switch {
			case o.o.Module == mod:
				continue
			case o.o.Module == "":
				o.o.Except = append(append([]string(nil), o.o.Except...), mod)
			}
			kept = append(kept, o)
		}
		out = append(kept, modOut...)
	}
	overrides := make([]genfile.Override, len(out))
	for i, m := range out {
		overrides[i] = m.o
	}
	return overrides, facts
}

// runManagedRules emits the overrides a sequence of rules gives, as
// buf reads the rules per file (REQ-migrate-gen): a value rule an
// override over its scope; a derivation from the file alone the
// same; a prefix or suffix rule the state the files had with its
// axis — one over its scope with the axis alone where no earlier
// override of the option covers the scope whole, then one over the
// meet with each earlier override of the option, in their order,
// carrying that override's other axis — an override a later one of
// the option covers whole dropped; a rule with an empty value, which
// clears what earlier rules set, unmapped. For a module's own run,
// each fact says so.
func runManagedRules(rules []managedRule, forModule string) ([]managedOverride, []Fact) {
	var out []managedOverride
	var facts []Fact
	report := func(f Fact) {
		if forModule != "" {
			f.Text += " (for the module " + forModule + ", under its disables)"
		}
		facts = append(facts, f)
	}
	spell := func(o genfile.Override) string {
		t := "overrides: files " + o.Files
		if o.Module != "" {
			t += " module " + o.Module
		}
		t += " option " + o.Option
		if !o.Derived() {
			return t + " value " + o.Value
		}
		if o.Prefix != "" {
			t += " prefix " + o.Prefix
		}
		if o.Suffix != "" {
			t += " suffix " + o.Suffix
		}
		return t
	}
	emit := func(key string, o genfile.Override, sc scope, note string) {
		out = append(out, managedOverride{o, sc, key})
		report(mapped(key, spell(o)+note))
	}
	// settle drops every override of an option a later one covers whole
	// — the later entry wins all its files — after a rule's overrides
	// are written, saying which a rule replaced.
	settle := func(key string) {
		kept := out[:0]
		var replaced []string
		for i, m := range out {
			covered := false
			for j := i + 1; j < len(out) && !covered; j++ {
				covered = out[j].o.Option == m.o.Option && out[j].scope.contains(m.scope)
			}
			if covered {
				replaced = append(replaced, m.key+"'s override of "+m.o.Option+" for files "+m.o.Files)
				continue
			}
			kept = append(kept, m)
		}
		out = kept
		if len(replaced) > 0 {
			report(mapped(key, "replacing "+strings.Join(replaced, ", ")))
		}
	}
	for _, r := range rules {
		switch r.kind {
		case ruleValue:
			o := r.sc.override(r.option)
			o.Value = r.value
			emit(r.key, o, r.sc, r.note)
			settle(r.key)
		case ruleBare:
			o := r.sc.override(r.option)
			o.Bare = true
			emit(r.key, o, r.sc, r.note)
			settle(r.key)
		case ruleDerive:
			option, isPrefix, _ := derivation(r.option)
			axis := "suffix"
			if isPrefix {
				axis = "prefix"
			}
			if r.value == "" {
				report(unmapped(r.key, "an empty "+axis+", clearing what buf's earlier rules set for the files: pb clears no override"))
				continue
			}
			set := func(o *genfile.Override) {
				if isPrefix {
					o.Prefix = r.value
				} else {
					o.Suffix = r.value
				}
			}
			type emission struct {
				o     genfile.Override
				scope scope
				note  string
			}
			other := map[bool]string{true: "suffix", false: "prefix"}[isPrefix]
			var adds []emission
			covered := false
			for _, m := range out {
				covered = covered || m.o.Option == option && m.scope.contains(r.sc)
			}
			if !covered {
				start := r.sc.override(option)
				set(&start)
				adds = append(adds, emission{start, r.sc, r.note})
			}
			for _, m := range out {
				if m.o.Option != option {
					continue
				}
				inter, ok := r.sc.meet(m.scope)
				if !ok {
					continue
				}
				o := inter.override(option)
				note := " (after " + m.key + "'s value, clearing the " + other + ")"
				if m.o.Derived() {
					o.Prefix, o.Suffix = m.o.Prefix, m.o.Suffix
					note = " (after " + m.key + ", which set no " + other + ")"
					if isPrefix && o.Suffix != "" || !isPrefix && o.Prefix != "" {
						note = " (with " + m.key + "'s " + other + ")"
					}
				}
				set(&o)
				// The last state over a scope is the files' state: an
				// earlier emission over the same scope is superseded
				// before it is written or reported.
				if n := len(adds); n > 0 && adds[n-1].scope == inter {
					adds = adds[:n-1]
				}
				adds = append(adds, emission{o, inter, note + r.note})
			}
			for _, e := range adds {
				emit(r.key, e.o, e.scope, e.note)
			}
			settle(r.key)
		}
	}
	return out, facts
}
