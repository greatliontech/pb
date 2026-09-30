package rules

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/greatliontech/pb/internal/contractfile"
)

// Function is a rule file's function (check-rules.md, the function
// term): a name, typed parameters, a return type and an expression
// over the parameters, compiled under the file's environment before
// its rules (REQ-rules-functions).
type Function struct {
	Name    string
	Params  []Param
	Returns Type
	CEL     string // the body as written
	// File is the rule file declaring the function, module-relative,
	// set when the ruleset's files are discovered together.
	File string
}

// Param is one typed parameter of a function.
type Param struct {
	Name string
	Type Type
}

// Type is a type spelling of the environment's vocabulary
// (REQ-env1-types): a name — a CEL scalar's, `dyn`, `null`, or a
// descriptor type's full name — with its arguments for `list` and
// `map`; the vocabulary is the environment's to judge at compile
// time, the shape the rule file's.
type Type struct {
	Name string
	Args []Type
}

// String spells the type as the rule file does.
func (t Type) String() string {
	if len(t.Args) == 0 {
		return t.Name
	}
	parts := make([]string, len(t.Args))
	for i, a := range t.Args {
		parts[i] = a.String()
	}
	return t.Name + "(" + strings.Join(parts, ", ") + ")"
}

// typeName is a type name's spelling: identifiers joined by dots.
var typeName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// ParseType reads a type spelling: a name, `list(T)` or `map(K, V)`,
// arguments spelled the same way; whether the environment provides
// the name is the environment's to judge.
func ParseType(s string) (Type, error) {
	s = strings.TrimSpace(s)
	open := strings.IndexByte(s, '(')
	if open < 0 {
		if !typeName.MatchString(s) || s == "list" || s == "map" {
			return Type{}, fmt.Errorf("type %q is no type spelling", s)
		}
		return Type{Name: s}, nil
	}
	name := s[:open]
	if !strings.HasSuffix(s, ")") || !typeName.MatchString(name) {
		return Type{}, fmt.Errorf("type %q is no type spelling", s)
	}
	var args []Type
	for _, a := range splitArgs(s[open+1 : len(s)-1]) {
		t, err := ParseType(a)
		if err != nil {
			return Type{}, err
		}
		args = append(args, t)
	}
	switch {
	case name == "list" && len(args) == 1, name == "map" && len(args) == 2:
		return Type{Name: name, Args: args}, nil
	}
	return Type{}, fmt.Errorf("type %q: %s takes no such arguments", s, name)
}

// splitArgs splits a parenthesized argument list at its top-level
// commas.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// identifier is a function's or a parameter's name: ASCII letters,
// digits and underscores, opening with a letter.
var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Scope is what a rule file's expressions see beside the environment
// (REQ-rules-functions, REQ-rules-imports): its own functions,
// unqualified; and the functions the rulesets it imports lend, as
// `<alias>.<name>` — each with the scope its own body compiles in.
// Every rule and function of a ruleset shares the ruleset's scope.
type Scope struct {
	Where     string // the file, for messages: <ruleset>'s <path>
	Functions []Function
	Imports   []Import
	// Lent is filled when the file's imports are read: by alias, the
	// functions the imported ruleset's rule files declare, each with
	// its own ruleset's scope.
	Lent map[string][]Lent
}

// Lent is one function an import lends, with the scope its body
// compiles in.
type Lent struct {
	Function Function
	Scope    *Scope
}

// parseFunctions reads the functions list: each `{name, params,
// returns, cel}`, names unique within the file, a function's
// parameter names unique within it, every type a type spelling.
func parseFunctions(n ast.Node) ([]Function, error) {
	var out []Function
	seen := map[string]bool{}
	err := contractfile.Sequence(n, "functions", ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("functions[%d]", i)
		var f Function
		err := contractfile.Mapping(en, where, ErrInvalid,
			contractfile.Field{Name: "name", Required: true, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok || !identifier.MatchString(text) {
					return fmt.Errorf("%w: %s: name must be ASCII letters, digits and underscores opening with a letter", ErrInvalid, where)
				}
				f.Name = text
				return nil
			}},
			contractfile.Field{Name: "params", Read: func(n ast.Node) error {
				names := map[string]bool{}
				return contractfile.Sequence(n, where+".params", ErrInvalid, func(j int, pn ast.Node) error {
					pwhere := fmt.Sprintf("%s.params[%d]", where, j)
					var p Param
					err := contractfile.Mapping(pn, pwhere, ErrInvalid,
						contractfile.Field{Name: "name", Required: true, Read: func(n ast.Node) error {
							text, ok := contractfile.Line(n)
							if !ok || !identifier.MatchString(text) {
								return fmt.Errorf("%w: %s: name must be ASCII letters, digits and underscores opening with a letter", ErrInvalid, pwhere)
							}
							p.Name = text
							return nil
						}},
						contractfile.Field{Name: "type", Required: true, Read: func(n ast.Node) error {
							text, ok := contractfile.Line(n)
							if !ok {
								return fmt.Errorf("%w: %s: type must be one line of text", ErrInvalid, pwhere)
							}
							t, err := ParseType(text)
							if err != nil {
								return fmt.Errorf("%w: %s: %v", ErrInvalid, pwhere, err)
							}
							p.Type = t
							return nil
						}},
					)
					if err != nil {
						return err
					}
					if names[p.Name] {
						return fmt.Errorf("%w: %s: parameter %q declared twice", ErrInvalid, pwhere, p.Name)
					}
					names[p.Name] = true
					f.Params = append(f.Params, p)
					return nil
				})
			}},
			contractfile.Field{Name: "returns", Required: true, Read: func(n ast.Node) error {
				text, ok := contractfile.Line(n)
				if !ok {
					return fmt.Errorf("%w: %s: returns must be one line of text", ErrInvalid, where)
				}
				t, err := ParseType(text)
				if err != nil {
					return fmt.Errorf("%w: %s: %v", ErrInvalid, where, err)
				}
				f.Returns = t
				return nil
			}},
			contractfile.Field{Name: "cel", Required: true, Read: func(n ast.Node) error {
				text, ok := contractfile.Scalar(n)
				if !ok || text == "" {
					return fmt.Errorf("%w: %s: cel must be a non-empty scalar", ErrInvalid, where)
				}
				f.CEL = text
				return nil
			}},
		)
		if err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("%w: %s: function %q declared twice", ErrInvalid, where, f.Name)
		}
		seen[f.Name] = true
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
