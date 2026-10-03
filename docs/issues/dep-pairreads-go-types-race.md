# The pair-reads guard races inside the type checker

`internal/dep` `TestPairReadsGoThroughTheDriver` loads the module's
packages through `golang.org/x/tools/go/packages` with types and
type information, which type-checks the packages concurrently. Under
the race detector on go1.27.1 the run reports a data race inside
`go/types` itself (`(*Checker).isComplete`, `cycles.go`) between two
packages' checkers, and the test fails: `go test -race -count=2 -run
TestPairReadsGoThroughTheDriver ./internal/dep/` reproduces it at
c9659c2 on this toolchain, not every run. Nothing of pb's runs in the
racing goroutines; the fault is the toolchain's or the loader's, and
pb cannot fix it in the test without serializing the loader's
checking, which `go/packages` does not offer.

Resolution: the toolchain or x/tools fixes the race, or the guard is
re-expressed over a loader that checks one package at a time.

Lands: `go test -race -count=5 -run TestPairReadsGoThroughTheDriver ./internal/dep/` passes on the project's toolchain.
