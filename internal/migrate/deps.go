package migrate

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
)

// Entry is a dependency table entry: the module path that is a BSR
// module's origin, and the BSR modules its files import — declared
// with it, since its origin is a synthesized module declaring nothing
// of its own. An entry with no path maps its name to nothing: the
// well-known types, the toolchain's own (generation.md
// REQ-gen-compile).
type Entry struct {
	Path string
	Deps []string
}

// WellKnownTypes is the BSR module of the well-known types, whose
// entry maps to nothing.
const WellKnownTypes = "buf.build/protocolbuffers/wellknowntypes"

// Dependencies is the dependency table (REQ-migrate-deps): a BSR module
// name to its entry, each entry's layout verified at entry by
// TestDependencyLayouts against the origins.
var Dependencies = map[string]Entry{
	"buf.build/bufbuild/protovalidate":         {Path: "github.com/bufbuild/protovalidate/proto/protovalidate"},
	"buf.build/cncf/xds":                       {Path: "github.com/cncf/xds", Deps: []string{"buf.build/envoyproxy/protoc-gen-validate", "buf.build/google/cel-spec", "buf.build/googleapis/googleapis"}},
	"buf.build/envoyproxy/envoy":               {Path: "github.com/envoyproxy/envoy/api", Deps: []string{"buf.build/cncf/xds", "buf.build/envoyproxy/protoc-gen-validate", "buf.build/googleapis/googleapis", "buf.build/opencensus/opencensus", "buf.build/opentelemetry/opentelemetry", "buf.build/prometheus/client-model"}},
	"buf.build/envoyproxy/protoc-gen-validate": {Path: "github.com/bufbuild/protoc-gen-validate"},
	"buf.build/google/cel-spec":                {Path: "github.com/google/cel-spec/proto", Deps: []string{"buf.build/googleapis/googleapis"}},
	"buf.build/googleapis/googleapis":          {Path: "github.com/googleapis/googleapis"},
	"buf.build/grpc-ecosystem/grpc-gateway":    {Path: "github.com/grpc-ecosystem/grpc-gateway", Deps: []string{"buf.build/googleapis/googleapis"}},
	"buf.build/grpc/grpc":                      {Path: "github.com/grpc/grpc-proto", Deps: []string{"buf.build/googleapis/googleapis"}},
	"buf.build/opencensus/opencensus":          {Path: "github.com/census-instrumentation/opencensus-proto/src"},
	"buf.build/opentelemetry/opentelemetry":    {Path: "github.com/open-telemetry/opentelemetry-proto"},
	"buf.build/prometheus/client-model":        {Path: "github.com/prometheus/client_model"},
	WellKnownTypes:                             {},
}

// Dep is a dependency replacement's target: the module path and, where
// the flag gave one, the version to declare with nothing discovered.
type Dep struct {
	Path    string
	Version string
}

// Replacements are the invocation's mappings (migrate.md, the
// replacement term): a BSR module name to a module path and perhaps a
// version, a BSR plugin name to a plugin reference, each winning over
// its table.
type Replacements struct {
	Deps    map[string]Dep
	Plugins map[string]string
}

// Replace records one `--dep` or `--plugin` value, `<name>=<target>`:
// refused where either side is empty, where the name is given twice,
// or where a dependency's target is no module path, with a version
// after `@` no version.
func (r *Replacements) Replace(flag, value string) error {
	name, target, ok := strings.Cut(value, "=")
	if !ok || name == "" || target == "" {
		return fmt.Errorf("--%s %q: expected <name>=<target>", flag, value)
	}
	switch flag {
	case "dep":
		path, v, versioned := strings.Cut(target, "@")
		if err := module.ValidatePath(path); err != nil {
			return fmt.Errorf("--dep %q: %v", value, err)
		}
		if versioned {
			if _, err := version.Parse(v); err != nil {
				return fmt.Errorf("--dep %q: %v", value, err)
			}
		}
		if r.Deps == nil {
			r.Deps = map[string]Dep{}
		}
		if _, dup := r.Deps[name]; dup {
			return fmt.Errorf("--dep %q: %s is replaced twice", value, name)
		}
		r.Deps[name] = Dep{Path: path, Version: v}
	case "plugin":
		if r.Plugins == nil {
			r.Plugins = map[string]string{}
		}
		if _, dup := r.Plugins[name]; dup {
			return fmt.Errorf("--plugin %q: %s is replaced twice", value, name)
		}
		r.Plugins[name] = target
	default:
		return fmt.Errorf("no replacement flag --%s", flag)
	}
	return nil
}

// Discovery names a module's versions: the tagged releases discovered
// for a path, ascending, and its latest version — the highest release
// or the origin's head as a pseudo-version (fetch.Client).
type Discovery interface {
	Versions(ctx context.Context, modPath string) ([]version.Version, error)
	Latest(ctx context.Context, modPath string) (version.Version, error)
}

// Deps declares the dependencies buf's configuration names in every
// module of the layout (REQ-migrate-deps): each `deps` entry of the
// buf.yaml at the directory — or, under a buf.work.yaml, of each
// member's — its `:ref` aside, looked up among the replacements and
// then in the table; a mapped path declared at the replacement's
// version or, discovered once per path, the latest version discovery
// names; a name neither holds, or one whose discovery fails, an
// unmapped fact naming the flag's form; a name the table maps to
// nothing — the well-known types — a mapped fact declaring nothing.
// A replacement naming what the configuration never declares fails.
// The lockfile's entries are read as declarations too: one the
// configuration declares already is a mapped fact naming the path it
// declared and that pb's pin is the tidy's, a BSR commit naming no
// git commit; one it does not — a dependency buf resolved for it —
// is declared the same way; its digest is read for nothing. The
// paths declared are recorded on the layout by BSR name (DepPaths).
func Deps(ctx context.Context, d Discovery, src *Source, lock *bufconfig.Lock, repl Replacements, l *Layout) ([]Fact, error) {
	if src == nil || l == nil {
		return nil, fmt.Errorf("no buf configuration read")
	}
	// An entry's source is its fact's: the configuration key and the
	// name as spelled, or a lock entry's key, which carries the name.
	type entry struct{ name, source string }
	var entries []entry
	configured := func(dep, key string) entry { return entry{dep, key + " " + dep} }
	if src.Work == nil && src.File != nil {
		for i, dep := range src.File.Deps {
			entries = append(entries, configured(dep, fmt.Sprintf("%s deps[%d]", bufconfig.FileName, i)))
		}
	}
	members := make([]string, 0, len(src.Members))
	for dir := range src.Members {
		members = append(members, dir)
	}
	sort.Strings(members)
	for _, dir := range members {
		for i, dep := range src.Members[dir].Deps {
			entries = append(entries, configured(dep, fmt.Sprintf("%s/%s deps[%d]", dir, bufconfig.FileName, i)))
		}
	}
	// The lock's entries, each a declaration where the configuration
	// has none for the name, after the configuration's own.
	type lockEntry struct {
		from string
		dep  bufconfig.Dep
	}
	var locked []lockEntry
	if lock != nil {
		for i, dep := range lock.Deps {
			locked = append(locked, lockEntry{fmt.Sprintf("%s deps[%d] %s %s", bufconfig.LockFileName, i, dep.Name, dep.Commit), dep})
		}
	}
	for _, dir := range sortedKeys(src.MemberLocks) {
		for i, dep := range src.MemberLocks[dir].Deps {
			locked = append(locked, lockEntry{fmt.Sprintf("%s deps[%d] %s %s", path.Join(dir, bufconfig.LockFileName), i, dep.Name, dep.Commit), dep})
		}
	}
	declares := map[string]bool{} // BSR name -> declared by the configuration or an earlier lock entry
	for _, e := range entries {
		name, _ := bsrSplit(e.name)
		declares[name] = true
	}
	for _, le := range locked {
		if !declares[le.dep.Name] {
			declares[le.dep.Name] = true
			entries = append(entries, entry{le.dep.Name, le.from})
		}
	}
	// A name the table holds brings the BSR modules its files import,
	// to closure: their origins are synthesized modules declaring
	// nothing, and the migration's declaration is the one that can
	// carry what they need (REQ-migrate-deps). A replacement gives the
	// name a path of its own; the table's knowledge of what the BSR
	// module imports stands regardless.
	named := map[string]bool{}
	for _, e := range entries {
		name, _ := bsrSplit(e.name)
		named[name] = true
	}
	for i := 0; i < len(entries); i++ {
		name, _ := bsrSplit(entries[i].name)
		for _, dep := range Dependencies[name].Deps {
			if named[dep] {
				continue
			}
			named[dep] = true
			entries = append(entries, configured(dep, name+"'s dependency"))
		}
	}
	// A replacement the configuration never names is refused first: a
	// stale flag is the likelier fault, and a conflict it takes part
	// in would name it as if it counted.
	declared := map[string]bool{Ruleset: true} // the ruleset's version may be pinned by its own path
	for name := range named {
		declared[name] = true
	}
	// A bundled import's provider, and a workspace module, may be
	// pinned by their own paths too, a version alone (Imports).
	for p := range pathKeyed(l) {
		declared[p] = true
		if r, ok := repl.Deps[p]; ok && (r.Path != p || r.Version == "") {
			return nil, fmt.Errorf("--dep %s=%s: a replacement keyed by a module path names a version alone, %s@<version>", p, r.Path+map[bool]string{true: "@" + r.Version, false: ""}[r.Version != ""], p)
		}
	}
	if r, ok := repl.Deps[Ruleset]; ok && r.Path != Ruleset {
		return nil, fmt.Errorf("--dep %s=%s: the lint file imports the ruleset %s; a replacement keyed by it names a version alone", Ruleset, r.Path, Ruleset)
	}
	if err := unusedReplacements("dep", repl.Deps, declared, "the configuration declares no such dependency"); err != nil {
		return nil, err
	}
	names := sortedKeys(repl.Deps)
	// A module path is declared at one version: a replacement's version
	// applies to every name reaching its path, two at odds refused.
	versions := map[string]string{} // module path -> the version declared
	pinned := map[string]string{}   // module path -> the replacement that pinned it
	for _, name := range names {
		r := repl.Deps[name]
		if r.Version == "" {
			continue
		}
		if prior, ok := versions[r.Path]; ok {
			if prior != r.Version {
				return nil, fmt.Errorf("--dep %s and --dep %s pin %s at %s and %s: a module path is declared at one version", pinned[r.Path], name, r.Path, prior, r.Version)
			}
			continue
		}
		versions[r.Path], pinned[r.Path] = r.Version, name
	}
	var facts []Fact
	l.DepPaths = map[string]string{}
	l.Versions = versions
	declaredAt := map[string]Fact{} // BSR name -> the fact its declaration made
	for _, e := range entries {
		name, _ := bsrSplit(e.name)
		path, from := "", "the dependency table"
		sibling := ""
		if r, ok := repl.Deps[name]; ok {
			path, from = r.Path, "--dep"
		} else if sp, ok := l.Names[name]; ok {
			// A BSR name a workspace module bears: the sibling
			// (REQ-migrate-imports).
			path, from = sp, "a workspace module, by its name"
		} else if te, ok := Dependencies[name]; ok && te.Path == "" {
			f := mapped(e.source, "nothing: the well-known types are the toolchain's own, never a dependency")
			facts = append(facts, f)
			declaredAt[name] = f
			continue
		} else {
			path = te.Path
		}
		if path == "" {
			f := unmapped(e.source, "no entry in the dependency table: pass --dep "+name+"=<module path>")
			facts = append(facts, f)
			declaredAt[name] = f
			continue
		}
		// A path a workspace module bears, however resolved, is the
		// sibling's: declared in every module but itself.
		for _, f := range l.Modules {
			if f.Module == path {
				sibling = path
			}
		}
		if by := pinned[path]; by != "" && by != name {
			from += ", at --dep " + by + "'s version"
		}
		v, ok := versions[path]
		if !ok {
			latest, err := d.Latest(ctx, path)
			if err != nil {
				f := unmapped(e.source, "no version discovered for "+path+" ("+oneLine(err.Error())+"): pass --dep "+name+"="+path+"@<version>")
				facts = append(facts, f)
				declaredAt[name] = f
				continue
			}
			v = latest.String()
			versions[path] = v
		}
		for _, f := range l.Modules {
			if f.Module == sibling {
				continue
			}
			if f.Deps == nil {
				f.Deps = map[string]string{}
			}
			f.Deps[path] = v
		}
		l.DepPaths[name] = path
		f := mapped(e.source, path+"@"+v+" ("+from+")")
		facts = append(facts, f)
		declaredAt[name] = f
	}
	// A lock entry the configuration declared already: the pin that
	// replaces buf's, the tidy's over the path declared.
	var lockFacts []Fact
	for _, le := range locked {
		at := declaredAt[le.dep.Name]
		if at.Source == le.from {
			continue // declared by this very entry, reported above
		}
		switch {
		case !at.Mapped:
			lockFacts = append(lockFacts, unmapped(le.from, "pinned nowhere: "+at.Text))
		case l.DepPaths[le.dep.Name] == "":
			lockFacts = append(lockFacts, mapped(le.from, at.Text))
		default:
			lockFacts = append(lockFacts, mapped(le.from, "pinned by the tidy in "+workspace.LockFileName+" over "+l.DepPaths[le.dep.Name]+" (a BSR commit names no git commit; pb's pin is the lockfile's own)"))
		}
	}
	// The ruleset the lint file imports (REQ-migrate-rules), at the
	// version a replacement pins or the highest discovered, declared
	// in no module file: a ruleset is no protobuf dependency.
	rulesetSource := "the lint file's rulesets " + Ruleset
	v, ok := versions[Ruleset]
	if !ok {
		latest, err := d.Latest(ctx, Ruleset)
		if err != nil {
			facts = append(facts, unmapped(rulesetSource, "no version discovered ("+oneLine(err.Error())+"): pass --dep "+Ruleset+"="+Ruleset+"@<version>"))
		} else {
			v, ok = latest.String(), true
		}
	}
	if ok {
		l.RulesetVersion = v
		from := "discovered"
		if pinned[Ruleset] != "" {
			from = "--dep"
		}
		facts = append(facts, mapped(rulesetSource, "rulesets: path "+Ruleset+" version "+v+" alias "+RulesetAlias+" ("+from+")"))
	}
	return append(facts, lockFacts...), nil
}
