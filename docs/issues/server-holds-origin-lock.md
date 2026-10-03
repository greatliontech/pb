# A long-lived server holds an origin's repository lock for its life

An origin's repositories in the direct source's store are held by one
process at a time, the lock beside them taken at opening and held to
the process's end (module-proxy.md REQ-proxy-direct-fetch); the fetch
client memoizes each repository's listing and each module path's
origin for the client's life (`internal/source/fetch/client.go`). A
command lives for one verb, so both bounds are the verb's. The
language server lives for the editor's session and keeps one client
across reloads (lsp.md REQ-lsp-session): in a fresh clone whose pinned
artifacts are not cached, the read-only session fetches them through
the direct source, takes the origin's lock, and holds it until the
editor closes — every `pb dep download`, `update` or `tidy` needing
that origin waits at its opening meanwhile, and fails with its
context — and a vanity redirect or a reference listing that changes
is not seen by the server until it restarts.

The lock's lifetime and the memo's are one contract to settle before
the fix: the clause names the process, and a server is a process
whose life is not a verb's. The shape that follows is a scope per
judgement — the server's client holding repositories, listings and
origins for one judgement or reload and releasing them at its end,
the direct store's lock released with the last repository held
under it, a verb's scope its whole run as now — stated in
module-proxy.md's clause (the lock held for a scope the holder
names, a verb's run or a server's judgement) and in lsp.md
REQ-lsp-session. Witnessed by a server whose judgement fetched
through the direct source and a verb that opens the same origin
afterwards without waiting, and by a redirect changed between two
reloads being followed by the second.

Lands: 33.
