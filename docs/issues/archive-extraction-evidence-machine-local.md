# Archive extraction mutation evidence stays machine-local

`internal/archive.ExtractZip` and `internal/archive.writeMember` cannot
produce committable mutation-test evidence. Their oracle tests create and
remove per-test directories under `internal/archive/testdata/scratch/`
(`localTempDir` in `zip_test.go`), so the observed runtime-input union of
the package directory differs across mutant executions. gomutant classes
every surviving mutant of these two targets `unstable-oracle`
("observation bracket moved: internal/archive") and keeps the records in
the machine-local overlay; at last measurement that is 37 survivors
(17 + 20) that cannot be attributed to weak assertions or equivalence.

Both escape routes are closed today:

- Scratch outside the module (`t.TempDir()`) was deliberately abandoned —
  `localTempDir`'s doc comment records the reason — because gomutant then
  observes an external directory surface and the evidence is unverifiable
  instead.
- Declaring the scratch root with `--bracket-path
  internal/archive/testdata/scratch` fails differently: the per-test
  subdirectories are transient (removed by test cleanup), so the bracket
  hash sees an "unhashable runtime directory".

The resolution is gomutant's declared scratch-namespace surface (its
`oracle-scratch-namespaces` issue; recover with
`git log --all -- docs/issues/oracle-scratch-namespaces.md` in the
gomutant repository), which will let oracles name in-module scratch roots
whose churn is excluded from union-equality. Until then the two targets'
records and any attestations on them are per-machine only and CI cannot
see them.

Lands: when gomutant can exclude a declared in-module scratch namespace
from oracle observation, or user decision on redesigning the extraction
tests' scratch strategy.
