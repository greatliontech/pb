package rules

import (
	"fmt"
	"regexp"

	"github.com/goccy/go-yaml/ast"

	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
)

// Import is one ruleset import (check-rules.md, the ruleset import
// term): the module path, the exact version to read at — none for a
// workspace module, read from the working tree — and the alias the
// importer names its rules, tags and functions by.
type Import struct {
	Path    string
	Version string
	Alias   string
}

// aliasForm is an alias's spelling: ASCII letters, digits and
// underscores, opening with a letter.
var aliasForm = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// CheckAlias refuses an alias outside its spelling.
func CheckAlias(alias string) error {
	if !aliasForm.MatchString(alias) {
		return fmt.Errorf("alias %q: an alias is ASCII letters, digits and underscores opening with a letter", alias)
	}
	return nil
}

// ParseImports reads a list of ruleset imports under key — the lint
// file's `rulesets`, a rule file's `imports` — each `{path, version,
// alias}`, the version optional, no alias repeated and no path
// repeated at one version; a rejection wraps invalid.
func ParseImports(n ast.Node, key string, invalid error) ([]Import, error) {
	var out []Import
	aliases, pairs := map[string]bool{}, map[string]bool{}
	err := contractfile.Sequence(n, key, invalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("%s[%d]", key, i)
		var imp Import
		line := func(name string, required bool, into *string, check func(string) error) contractfile.Field {
			return contractfile.Field{Name: name, Required: required, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok || text == "" {
					return fmt.Errorf("%w: %s.%s must be a non-empty line of text", invalid, where, name)
				}
				if err := check(text); err != nil {
					return fmt.Errorf("%w: %s.%s: %v", invalid, where, name, err)
				}
				*into = text
				return nil
			}}
		}
		err := contractfile.Mapping(en, where, invalid,
			line("path", true, &imp.Path, module.ValidatePath),
			line("version", false, &imp.Version, func(v string) error { _, err := version.Parse(v); return err }),
			line("alias", true, &imp.Alias, CheckAlias),
		)
		if err != nil {
			return err
		}
		if aliases[imp.Alias] {
			return fmt.Errorf("%w: %s: alias %s is another import's", invalid, where, imp.Alias)
		}
		if pairs[imp.Path+"@"+imp.Version] {
			return fmt.Errorf("%w: %s: %s at this version is imported twice", invalid, where, imp.Path)
		}
		aliases[imp.Alias], pairs[imp.Path+"@"+imp.Version] = true, true
		out = append(out, imp)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
