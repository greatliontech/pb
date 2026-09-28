# A fifth reader of a build-list pair can bypass the replacement

Lands: the replace plan's chunk 2 (docs/plans/replace.md): the
directory form adds the second kind of source a reader must take, the
shape to build the one reader around

Every reader of a build-list pair asks `workspace.Root.Source` for
the pair whose module file and archive answer for it, then calls the
fetch client with the answer: selection's requirement loader
(internal/resolve/resolve.go), the file sets' archive loader and
`download` (internal/dep/dep.go), and tidy's reachable pins
(internal/dep/tidy.go). The discipline is by convention at four call
sites: a new verb calling `s.Client.Zip(r.Path, r.Version)` on a
build-list pair compiles and silently fetches the replaced path,
which REQ-work-replace forbids. The tests pin the four readers, not
the absence of a fifth.

The collapse is a reader that takes build-list pairs and applies the
mapping itself — the fetch client's module-file and archive entry
points wrapped over the root, or the driver owning the only handle to
the client for pair reads — so a bypass cannot be written. Tidy's use
is not a fetch (it maps edge pairs to pin keys) and stays a caller of
`Source`. The directory form gives a source that is a working tree
rather than a fetched pair, which is the second shape the reader must
serve; building the reader with both shapes known avoids a retrofit.
