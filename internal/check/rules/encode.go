package rules

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/greatliontech/pb/internal/contractfile"
)

// Encode renders a rule file canonically (REQ-rules-emission): celEnv,
// imports and functions where any, rules — `[]` where none — entries
// in the order given, an import's path, version (absent for a
// workspace module) and alias, a function's name, params (`[]` where
// none, each name then type), returns and cel, a rule's id, kind,
// target, severity, tags (absent where none), cel and message, celEnv
// as unquoted digits and every other scalar spelled as
// contractfile.Spell has it. The rendering is held to its reading —
// Encode never emits what Parse rejects, nor what Parse reads as a
// different file.
func Encode(f *File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: no file", ErrInvalid)
	}
	return contractfile.Emit(func(w *contractfile.Writer) {
		w.Literal("celEnv", strconv.Itoa(f.CELEnv))
		if len(f.Imports) > 0 {
			w.Sequence("imports", len(f.Imports), func(i int) {
				imp := f.Imports[i]
				w.Scalar("path", imp.Path)
				if imp.Version != "" {
					w.Scalar("version", imp.Version)
				}
				w.Scalar("alias", imp.Alias)
			})
		}
		if len(f.Functions) > 0 {
			w.Sequence("functions", len(f.Functions), func(i int) {
				fn := f.Functions[i]
				w.Scalar("name", fn.Name)
				if len(fn.Params) == 0 {
					w.List("params", nil)
				} else {
					w.Sequence("params", len(fn.Params), func(j int) {
						w.Scalar("name", fn.Params[j].Name)
						w.Scalar("type", fn.Params[j].Type.String())
					})
				}
				w.Scalar("returns", fn.Returns.String())
				w.Scalar("cel", fn.CEL)
			})
		}
		if len(f.Rules) == 0 {
			w.List("rules", nil)
			return
		}
		w.Sequence("rules", len(f.Rules), func(i int) {
			r := f.Rules[i]
			w.Scalar("id", r.ID)
			w.Scalar("kind", string(r.Kind))
			w.Scalar("target", string(r.Target))
			w.Scalar("severity", string(r.Severity))
			if len(r.Tags) > 0 {
				w.List("tags", r.Tags)
			}
			w.Scalar("cel", r.CEL)
			w.Scalar("message", r.Message)
		})
	}, Parse, f, sameFile, ErrInvalid)
}

// sameFile compares two rule files by what they declare — the
// environment, the imports, the functions and the rules — the scope
// and the discovering file aside.
func sameFile(a, b *File) bool {
	sameImport := func(x, y Import) bool { return x.Path == y.Path && x.Version == y.Version && x.Alias == y.Alias }
	sameParam := func(x, y Param) bool { return x.Name == y.Name && x.Type.String() == y.Type.String() }
	sameFunction := func(x, y Function) bool {
		return x.Name == y.Name && slices.EqualFunc(x.Params, y.Params, sameParam) && x.Returns.String() == y.Returns.String() && x.CEL == y.CEL
	}
	sameRule := func(x, y Rule) bool {
		return x.ID == y.ID && x.Kind == y.Kind && x.Target == y.Target && x.Severity == y.Severity && slices.Equal(x.Tags, y.Tags) && x.CEL == y.CEL && x.Message == y.Message
	}
	return a.CELEnv == b.CELEnv && slices.EqualFunc(a.Imports, b.Imports, sameImport) && slices.EqualFunc(a.Functions, b.Functions, sameFunction) && slices.EqualFunc(a.Rules, b.Rules, sameRule)
}
