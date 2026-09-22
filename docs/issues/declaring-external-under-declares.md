# A declaring external that under-declares wedges tidy

Lands: when a module archive with a module file is served whose
files import a path its declarations do not reach, in a fixture or
an origin — the tidy carrying the consumer's added declaration for
it, or refusing the module by name at load

The tidy carries what a synthesized module's files import, since
such a module declares nothing (dep-verbs.md REQ-dep-tidy). An
external whose archive holds a module file contributes its own
declarations as the graph's edges and is carried for nothing: one
whose files import a path its declarations do not reach leaves the
consumer no repair — the consumer adds the provider, the tidy drops
it as an import no file of the consumer or of a synthesized module
uses, and the next round's import check fails naming the external's
file. The failure is loud and names the module at fault, and the
module is the fault's owner; what pb should do — carry the addition
as it carries a synthesized module's needs, or refuse the module at
load as one that does not stand alone — is decided when the shape
is met.
