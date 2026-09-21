# Two catalog rules are quadratic for want of a grouping primitive

Lands: user decision

buf's `DIRECTORY_SAME_PACKAGE` (the files of one directory declare one
package) and `RPC_REQUEST_RESPONSE_UNIQUE` (every request and
response message is used by one rpc) group the schema's files or
rpcs by a key. CEL under environment 1 has no linear grouping: cel-go
charges `distinct()` and `sort()` alike by the square of the list
(one self-comparison tracker serves both), and its map comprehension
`transformMapEntry` refuses a duplicate key rather than collapsing
it. The corpus writes both rules through `distinct()`; they are
correct at any size and exceed REQ-rules-bounded's cost limit,
failing the run rather than judging, over a schema of some ten
thousand files or rpcs.

The fork. One: environment 1 gains a linear grouping primitive of
pb's own, a function charged by the size of its input that reports a
list's duplicates or its distinct members, so a rule over the whole
set costs what the set costs as REQ-rules-bounded intends; the two
rules become linear. Two: the environment stays as specified and the
two rules keep their cost, disclosed in the corpus. The tradeoff the
user weighs: one function of pb's in the environment's contract,
against buf's two grouping rules failing on the largest workspaces.
