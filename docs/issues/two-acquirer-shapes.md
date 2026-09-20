# The local and oci acquirers have parallel shapes with one caller

Lands: package-structure plan chunk 8

The generate verb takes two acquirers: dep.Acquirer for oci-scheme
entries, returning an oci.Acquired (the process, the pin, the image's
digest and evidence), and dep.LocalAcquirer for local-scheme entries,
returning a local.Acquired (the process, the pin). The two results
share the process and the pin; the verb reads those and the oci
result's image fields for the lockfile's provenance record. Two
interfaces with one caller are the parallel mechanism the
consolidation scan names.

The shape that keeps every field honest and gives the verb one seam:
one Acquired in a subpackage of the domain — not the root, since it
carries the lockfile's plugin pin and the lockfile imports the root —
with the process and the pin as its fields and the image's facts
behind a pointer the local scheme leaves nil, so a reader cannot take
an absent image for an empty one; one dep.Acquirer keyed by the
entry's scheme; each acquirer returning what its scheme yields in
that one type. A flat struct with empty image fields for a local
binary, and the two shapes kept, were the alternatives considered:
the first trades the fields' honesty for the seam, the second the
seam for the fields, and the tagged shape keeps both.
