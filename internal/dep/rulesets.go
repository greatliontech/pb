package dep

import (
	"context"
	"fmt"

	"github.com/go-git/go-billy/v6/helper/iofs"
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

// closure reads the lint file's imports through the rule files' own
// (REQ-dep-ruleset-declarations): every import an edge, every fetched
// pair — pinned as a ruleset on the way, the pins saved — what
// download fetches and tidy keeps.
func (s *Session) closure(ctx context.Context) (*lintfile.Loaded, error) {
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	loaded, err := lintfile.Load(ctx, lf, s.Root, iofs.New(s.WS), s.Client.RulesetZip)
	if err := savePins(s, err); err != nil {
		return nil, err
	}
	return loaded, nil
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
