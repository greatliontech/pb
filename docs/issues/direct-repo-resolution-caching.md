# Direct source: per-call O(repo) recomputation in pseudo-version resolution

Lands: 13

`direct.Repo.resolvePseudo` recomputes, on every call against the same
fetched repository: the full ref listing (`Refs`), a full commit-object
scan (`commitsWithPrefix`), and an ancestry walk (`ancestors`). Correct
but repeated work — a driver resolving many versions against one `Repo`
(dep verbs: tidy, download, graph) pays it per version.

Collapse sketch: memoize on `Repo` — the fetched storage is immutable
after `Fetch`, so a lazily-built ref snapshot, a hash→commit index (which
also serves prefix lookup), and a per-head ancestry cache are all
write-once. No mechanism changes: `resolvePseudo`'s reads route through
the cached views; the pure decision functions (`uniqueCommit`,
`expectedPseudo`'s scan) stay as they are. Invariants preserved:
resolution stays a pure function of fetched repo state — caching a pure
function changes cost, never results.

Deferred because the caching design (laziness, what indexes earn their
memory) should be shaped by the driver's actual access pattern, which
chunk 13 materializes.
