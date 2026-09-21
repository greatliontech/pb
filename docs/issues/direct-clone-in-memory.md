# The direct construction clones an origin's whole history into memory

Lands: the migrate plan's chunk 8

The direct construction fetches an origin as a bare clone in memory,
every branch and tag (`internal/source/direct`): hermetic, and the
size of the origin's whole history. An origin the size of googleapis —
the first dependency the migration maps, and one every grpc-gateway,
envoy, xds and grpc module depends on — is hundreds of megabytes of
history, and the clone ends the process on a host with a memory guard
before a version is discovered; a user's `pb dep tidy` against such an
origin meets the same clone.

What an operation needs of history is exact, and shaped by the proof
rather than small: a listing needs the refs alone; a tagged release or
a commit needs that commit's tree, depth one; a pseudo-version needs
its commit and whatever history decides its base — the highest release
tag among the commit's ancestors, no higher tag an ancestor
(`module-resolution.md` REQ-resolve-pseudo-base) — which, where every
release tag lies off the commit's line, is the commit's ancestry down
to the fork, most of the mainline, and for that case disk rather than
memory is the remedy. Chunk 8 derives that fetch under its own
spec-first gate, with two facts already in hand: deepening until the
claimed tag appears shows it an ancestor and nothing about a higher
one; and a shallow fetch's boundary is a commit, not necessarily a
tag's, and a tag's own ancestry is not the commit's, so the decision
is over the commit's ancestor set as fetched, not over tags found
along the way. A fixed default depth, as buf's git inputs carry, would
be a heuristic: the proof succeeding or failing by how far back the
tags lie. The fetch is bounded by what the proof needs, into a bare
repository kept in the module cache on disk and reused by later
resolutions of the origin, as Go's VCS cache is: history costs what
the proofs required, on disk, once.
