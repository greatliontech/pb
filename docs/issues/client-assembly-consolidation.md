# Client assembly: three construction sites

Lands: user decision

`modfetch.Client` is assembled at three sites: `cmd/pb`'s
`assembleClient` (environment-driven), and the two test wrappers in
`internal/modfetch` and `internal/resolve`/`internal/dep` over the
shared `internal/modfetchtest` fixture parts. The test copies exist
because `modfetchtest` cannot import `modfetch` (the modfetch suite
would form an import cycle through it), so each consumer closes the
six-field literal itself.

Collapse sketch: either a cycle-breaking assembly sub-package
(importing both `modfetchtest` and `modfetch`, usable by every suite
except modfetch's own) or a production options constructor
(`modfetch.NewClient`) that all sites call. Both are heavier than the
duplicated literal today; the fold pays for itself if assembly grows
defaults or validation.
