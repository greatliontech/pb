# The check subsystem's mutation evidence stays machine-local

Lands: the next change set touching the breaking package's tests, the
generate verb's symlink test, or the command layer's session tests
(the oracles' reads outside the tree stop there), or gomutant serving
records whose oracle reads the filesystem root

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
  directory in no repository, walks go-git's `.git` detection up to
  `/` and probes it as a bare repository (`/.git`, `/HEAD`,
  `/config`, `/packed-refs`); go-git's repository open also reads the
  user's `~/.gitconfig` and probes `/etc/gitconfig`, two inputs the
  tree cannot vouch for;
- `dep.TestGenSymlinkEscapeRefused` roots a billy filesystem at `/`
  for its absolute-path halves;
- `cmd/pb.TestClientSettingsNameTheirLayer` is named beside them by
  gomutant's guidance.

Under an unstable oracle the open mutants are not findings: a probe
of one, `env1.Program.eval`'s error return dropped, is killed by
`TestEvalVerdicts`. The 244 open survivors therefore await a stable
oracle before any is dispositioned; the interrupted rule-file
campaign's dirty-tree records (12 open, at lines that no longer
exist) are superseded by the same re-measurement.

What stabilizes the oracles: `RepoOf`'s walk bounded so a test's
outside case never reaches `/` (git's own `GIT_CEILING_DIRECTORIES`
is the precedent, a spec clause if adopted); the repository tests
isolating `HOME` and `XDG_CONFIG_HOME`, with go-git's system
configuration probe still to be avoided or vouched; the symlink test
rooted in its scratch directory rather than `/`. Then the campaign
re-runs and its records promote.
