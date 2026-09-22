package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
)

// Entry is a dependency table entry: the module path that is a BSR
// module's origin, and the BSR modules its files import — declared
// with it, since its origin is a synthesized module declaring nothing
// of its own.
type Entry struct {
	Path string
	Deps []string
}

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
// unmapped fact naming the flag's form. A replacement naming what the
// configuration never declares fails. The lockfile's entries are
// unmapped facts: pb's pin is the tidy's.
func Deps(ctx context.Context, d Discovery, src *Source, lock *bufconfig.Lock, repl Replacements, l *Layout) ([]Fact, error) {
	if src == nil || l == nil {
		return nil, fmt.Errorf("no buf configuration read")
	}
	type entry struct{ name, from string }
	var entries []entry
	if src.Work == nil && src.File != nil {
		for i, dep := range src.File.Deps {
			entries = append(entries, entry{dep, fmt.Sprintf("%s deps[%d]", bufconfig.FileName, i)})
		}
	}
	members := make([]string, 0, len(src.Members))
	for dir := range src.Members {
		members = append(members, dir)
	}
	sort.Strings(members)
	for _, dir := range members {
		for i, dep := range src.Members[dir].Deps {
			entries = append(entries, entry{dep, fmt.Sprintf("%s/%s deps[%d]", dir, bufconfig.FileName, i)})
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
			entries = append(entries, entry{dep, name + "'s dependency"})
		}
	}
	// A replacement the configuration never names is refused first: a
	// stale flag is the likelier fault, and a conflict it takes part
	// in would name it as if it counted.
	declared := map[string]bool{Ruleset: true} // the ruleset's version may be pinned by its own path
	for name := range named {
		declared[name] = true
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
	for _, e := range entries {
		name, _ := bsrSplit(e.name)
		path, from := "", "the dependency table"
		if r, ok := repl.Deps[name]; ok {
			path, from = r.Path, "--dep"
		} else {
			path = Dependencies[name].Path
		}
		if path == "" {
			facts = append(facts, unmapped(e.from+" "+e.name, "no entry in the dependency table: pass --dep "+name+"=<module path>"))
			continue
		}
		if by := pinned[path]; by != "" && by != name {
			from += ", at --dep " + by + "'s version"
		}
		v, ok := versions[path]
		if !ok {
			latest, err := d.Latest(ctx, path)
			if err != nil {
				facts = append(facts, unmapped(e.from+" "+e.name, "no version discovered for "+path+" ("+oneLine(err.Error())+"): pass --dep "+name+"="+path+"@<version>"))
				continue
			}
			v = latest.String()
			versions[path] = v
		}
		for _, f := range l.Modules {
			if f.Deps == nil {
				f.Deps = map[string]string{}
			}
			f.Deps[path] = v
		}
		facts = append(facts, mapped(e.from+" "+e.name, path+"@"+v+" ("+from+")"))
	}
	// The ruleset the lint file imports, declared by every module
	// (REQ-migrate-rules) at the version pinned or discovered.
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
		for _, f := range l.Modules {
			if f.Deps == nil {
				f.Deps = map[string]string{}
			}
			f.Deps[Ruleset] = v
		}
		from := "discovered"
		if pinned[Ruleset] != "" {
			from = "--dep"
		}
		facts = append(facts, mapped(rulesetSource, Ruleset+"@"+v+" ("+from+"; the ruleset, declared by every module)"))
	}
	if lock != nil {
		for i, dep := range lock.Deps {
			facts = append(facts, unmapped(fmt.Sprintf("%s deps[%d] %s %s", bufconfig.LockFileName, i, dep.Name, dep.Commit), "a BSR commit names no git commit; pb's pin is the lockfile's own, made by the tidy"))
		}
	}
	return facts, nil
}
