# go1.27.1's go/types races under go/packages' concurrent loader

`internal/dep`'s TestPairReadsGoThroughTheDriver loads every package
of the module through go/packages with syntax and types. Under
go1.27.1 the race detector reports a read of an instantiated Named
type's right-hand side (`go/types/cycles.go`, `Checker.isComplete`)
against its write under the type's lock (`go/types/named.go`,
`Named.unpack`) from two root packages type-checked at once: every
fifteen-iteration process under `-race -count=15` reports it, on this
host and on the CI row. Under go1.26.6, the toolchain go.mod
declares, the same process is clean; golang.org/x/tools v0.49.0 and
v0.50.0 race alike, the fault being the standard library's. CI runs
the suite on the module's toolchain meanwhile (`go-version-file:
go.mod`), the spec verification's witnesses pinned to it too; at the
landing CI moves back to the current release.

Lands: when a Go release whose go/types reads an instantiated Named's
right-hand side under its lock is the module's toolchain, a
fifteen-iteration process under the detector clean on it.
