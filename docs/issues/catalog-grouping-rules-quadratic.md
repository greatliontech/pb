# Two catalog rules are quadratic for want of a grouping primitive

Lands: user decision

buf's `DIRECTORY_SAME_PACKAGE` (the files of one directory declare one
package) and `RPC_REQUEST_RESPONSE_UNIQUE` (every request and
response message is used by one rpc) group the schema's files or
rpcs by a key. CEL under environment 1 has no map comprehension and
no set, so the corpus writes both through `distinct()`, which cel-go
charges by the square of the list; the rules are correct at any
size and exceed REQ-rules-bounded's cost limit, failing the run
rather than judging, over a schema of some ten thousand files or
rpcs.

The fork. One: environment 1 gains a linear grouping primitive, a
function charged by the size of its input that reports a list's
duplicates or groups its elements by a key expression, so a rule
over the whole set costs what the set costs as REQ-rules-bounded
intends; the two rules become linear. Two: the environment stays as
specified and the two rules keep their cost, disclosed in the
corpus. The tradeoff the user weighs: one more function in the
environment's contract, against buf's two grouping rules failing on
the largest workspaces.
