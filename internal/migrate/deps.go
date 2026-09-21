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

// Dependencies is the dependency table (REQ-migrate-deps): a BSR module
// name to the module path that is its origin, each entry's layout
// verified at entry by TestDependencyLayouts against the origins.
var Dependencies = map[string]string{
	"buf.build/opentelemetry/opentelemetry": "github.com/open-telemetry/opentelemetry-proto",
	"buf.build/prometheus/client-model":     "github.com/prometheus/client_model",
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
	// A replacement the configuration never names is refused first: a
	// stale flag is the likelier fault, and a conflict it takes part
	// in would name it as if it counted.
	declared := map[string]bool{}
	for _, e := range entries {
		name, _, _ := strings.Cut(e.name, ":")
		declared[name] = true
	}
	names := make([]string, 0, len(repl.Deps))
	for name := range repl.Deps {
		names = append(names, name)
	}
	sort.Strings(names)
	var unused []string
	for _, name := range names {
		if !declared[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		return nil, fmt.Errorf("--dep %s: the configuration declares no such dependency", strings.Join(unused, ", --dep "))
	}
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
		name, _, _ := strings.Cut(e.name, ":")
		path, from := "", "the dependency table"
		if r, ok := repl.Deps[name]; ok {
			path, from = r.Path, "--dep"
		} else {
			path = Dependencies[name]
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
	if lock != nil {
		for i, dep := range lock.Deps {
			facts = append(facts, unmapped(fmt.Sprintf("%s deps[%d] %s %s", bufconfig.LockFileName, i, dep.Name, dep.Commit), "a BSR commit names no git commit; pb's pin is the lockfile's own, made by the tidy"))
		}
	}
	return facts, nil
}
