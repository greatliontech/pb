# Archive extraction mutation evidence stays machine-local

`internal/archive.ExtractZip` and `internal/archive.writeMember`
cannot yet produce committable mutation-test evidence. Their oracle
tests create and remove per-test directories under
`internal/archive/testdata/scratch/` (`localTempDir` in
`zip_test.go`), which gomutant now takes as a declared scratch
namespace: a campaign over the two targets with
`--scratch-namespace internal/archive/testdata/scratch:x` reaches
measurement (100 mutants generated, 47 killed, 37 open) and the
scratch churn no longer moves the observation bracket. The records
stay machine-local for two reasons the declaration does not touch,
both in the analysis layer (gofresh, through gomutant):

- the target's runtime inputs are classed "external directory input:
  /" while the record's own input list is empty and a syscall trace
  of the same tests touches no path named `/`;
- the observation's subject reachability is not closed: a computed
  function call in `validatePath` (the `strings.SplitSeq` iterator a
  range statement calls) and an interface invoke outside the
  program's type analysis in `writeMember` (`hash.Hash.Sum` on a
  standard-library hash).

Until those clear, the two targets' 37 open survivors and any
attestations on them are per-machine only and continuous
integration cannot see them. A first campaign over this tree also
spends its whole budget in freshness proofs (a union over 486
subjects for two targets); the proof slices it persists let a rerun
reach measurement.

Lands: gomutant serves the two targets' records as repo evidence —
the `/` classification and the open reachability resolved on its
side (reported to the maintainer)
