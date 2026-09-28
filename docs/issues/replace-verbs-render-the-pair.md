# graph and why render nothing of a replacement

Lands: the replace plan's chunk 2 (docs/plans/replace.md): the
directory form, so both forms' rendering settle together

With `replace: {example.com/x: example.com/y@v1.0.0}` in the
workspace file, `pb dep why example.com/y` answers "(module
example.com/y is not needed)" although y is fetched, pinned and
load-bearing — it is the source of every x node — and `pb dep graph`
prints x's edges (read from y's module file) with nothing saying
where they came from. Both verbs follow REQ-dep-why and REQ-dep-graph
as written: they answer over the requirement graph, where y has no
node, and `download` is the one verb that names the pair
(`x@v => y@v1`). Go's `go mod why` on a replacement target answers
the same way, so the output has precedent, but "not needed" is false
of a pinned module, and a reader of `graph` cannot tell that x's
requirements are the fork's.

Settling it means a spec clause per verb: `why Y` for a replacement
path answering with the chain to each X it stands for and the
replacement step last, or restating that Y is asked about through X;
`graph` either as today or with the replaced pair spelled on x's
nodes. The directory form's rendering is the same question with a
directory in Y's place, so both settle at once.
