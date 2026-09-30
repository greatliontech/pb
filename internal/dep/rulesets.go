package dep

import (
	"fmt"

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
)

// The lint file as the graph names it: the requirer of every ruleset
// import (REQ-dep-ruleset-declarations).
const lintRequirer = lintfile.FileName

// rulesetImport is one import of the lint file as the verbs act on
// it (REQ-dep-ruleset-declarations): the import and what it reads,
// as the check run reads it.
type rulesetImport struct {
	imp rules.Import
	lintfile.Resolved
}

// imports reads the lint file's ruleset imports, none where the file
// is absent, each classified as the check run classifies it; an
// import the check run would refuse is refused here too, naming the
// file.
func (s *Session) imports() ([]rulesetImport, error) {
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	var out []rulesetImport
	for _, imp := range lf.Rulesets {
		res, err := lintfile.Resolve(s.Root, imp)
		if err != nil {
			return nil, fmt.Errorf("%s: rulesets %v", lintfile.FileName, err)
		}
		out = append(out, rulesetImport{imp, res})
	}
	return out, nil
}
