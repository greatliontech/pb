package dep

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/module/version"
)

// The lint file as the graph names it: the requirer of every ruleset
// import (REQ-dep-ruleset-declarations).
const lintRequirer = lintfile.FileName

// rulesetImport is one import of the lint file as the verbs act on
// it (REQ-dep-ruleset-declarations): the import and what it reads,
// as the check run reads it — or, versionless, a fetched import
// written without a version, read at none, which the update alone
// admits.
type rulesetImport struct {
	imp rules.Import
	lintfile.Resolved
	versionless bool
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

// importsToMove reads the lint file's ruleset imports as the update
// verb moves them, each classified as the check run classifies it —
// an import the check run would refuse is refused here too, naming
// the file — but a fetched import written without a version, which
// a migration whose discovery found none writes (migrate.md
// REQ-migrate-rules): a requirement with no release at all, read at
// no version, which the update moves to the highest discovered
// (REQ-dep-update).
func (s *Session) importsToMove() ([]rulesetImport, error) {
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	var out []rulesetImport
	for _, imp := range lf.Rulesets {
		res, err := lintfile.Resolve(s.Root, imp)
		versionless := errors.Is(err, lintfile.ErrNoVersion)
		if versionless {
			res, err = lintfile.Resolved{Source: s.Root.Source(imp.Path, version.Version{})}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: rulesets %v", lintfile.FileName, err)
		}
		out = append(out, rulesetImport{imp, res, versionless})
	}
	return out, nil
}
