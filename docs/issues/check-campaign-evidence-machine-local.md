# The check subsystem's mutation evidence stays machine-local

Lands: the check subsystem's campaign banks repo-committable records
on the tree whose oracles read nothing outside it

The delta campaign over the check subsystem's eight chunks measured
on a clean tree: 87 of 102 targets banked in eighty-eight minutes
(1233 mutants killed, 244 open) before the host's memory guard ended
it, a resumption re-measuring rather than serving the banked prefix.
Every record is machine-local, its runtime evidence unverifiable:
three oracle tests read the filesystem root, which gomutant's bracket
cannot fingerprint (a volatile OS root is refused as a bracket path),
so the oracle's evidence is unstable and no record can be served or
committed. A syscall trace confirms the reads are real, not the
analysis layer's misclassification the archive issue records:

- `breaking.TestFromRef`'s outside case, `RepoOf` over a temporary
  directory in no repository, walked go-git's `.git` detection up to
  `/` and probed it as a bare repository (`/.git`, `/HEAD`,
  `/config`, `/packed-refs`), the probes of the temporary directory's
  ancestors under `/tmp` the record's machine-local inputs there;
  the fixture's commits read the user's `~/.gitconfig` and probed
  `/etc/gitconfig`;
- `dep.TestGenSymlinkEscapeRefused` rooted a billy filesystem at `/`
  for its absolute-path halves;
- `cmd/pb.TestClientSettingsNameTheirLayer` was named beside them by
  gomutant's guidance; a syscall trace of the test shows no read of
  `/`, the naming the oracle group's, not the test's.

Under an unstable oracle the open mutants are not findings: a probe
of one, `env1.Program.eval`'s error return dropped, is killed by
`TestEvalVerdicts`. The 244 open survivors therefore await a stable
oracle before any is dispositioned; the interrupted rule-file
campaign's dirty-tree records (12 open, at lines that no longer
exist) are superseded by the same re-measurement.

What stabilized the oracles: `RepoOf` searches for `.git` itself,
bounded by `GIT_CEILING_DIRECTORIES` as git's search is, the tests
setting the ceiling at their scratch root; the breaking fixture is
built through the object store alone, no commit and no
configuration consulted; the symlink test roots its filesystem at
an in-tree scratch directory. A trace of both packages' tests shows
no read outside the tree. The campaign re-runs on that tree and its
records promote, closing this issue.
