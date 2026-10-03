# The module cache names an entry by its pair alone

`<cache>/<escaped module path>/@v/<escaped version>.<kind>`
(dep-verbs.md REQ-dep-cache-layout) is a content name: an entry's
bytes are meant never to change under it, and the cache is shared by
every root on the machine. Nothing makes one pair mean one content
across roots, though: pb has no checksum database, so two roots that
pinned one pair at first use on either side of a moved tag hold two
digests for it, each correct against its own origin, and the cache
can serve only one. Correctness holds — every read verifies against
the reading root's pin, a failing entry is discarded and refetched
(REQ-dep-cache-transparent) — but the entry flips between the two
contents at each root's refetch, every read on the other root pays a
fetch, and `pb dep verify` in one root convicts the cache for the
other root's bytes rather than for corruption. A first use leaves an
entry present as it is, so a root's bytes are never rewritten by
another root's first use; the flip remains on the discard-refetch
path, where the spec admits the rewrite.

Go's module cache has the same layout and avoids the case because
the checksum database makes (path, version) mean one content
everywhere. pb's answer is the one a content name demands where no
global agreement exists: the entry's name carries the digest —
`<escaped version>.<digest>.<kind>` or a digest directory under
`@v/` — so two roots' contents coexist and each root reads its own
pin's entry; `info` has no digest and stays version-addressed; the
language server's dependency source store, laid out as the cache is
(lsp.md REQ-lsp-dependency-files), takes the same key; `pb dep
clean` and the store's layout recognition (`internal/dep/clean.go`,
`fetch.IsArtifactName`) read the new names. Spec: REQ-dep-cache-layout
amended to the digest-carrying name, REQ-dep-cache-transparent stating
that two roots' pins of one pair are two entries; verify's report
naming the pin's entry alone. An existing cache is regenerable: an old
entry is not recognized and is cleaned, a read refetches.

Lands: 32.
