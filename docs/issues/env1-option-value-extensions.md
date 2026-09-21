# An extension within a message-valued option's value is beyond a rule's reach

Lands: a rule needs it — a catalog rule or a user's rule reported
selecting an extension of a message-valued custom option's value

Environment 1 reads a custom option through
`proto.getExt(entity.options, pkg.ext)` and a language feature
through `proto.getExt(features(entity), pb.java)`, every descriptor
a rule names rebuilt over the Go runtime's descriptor family, the
one the registry reads extensions from (check-rules.md
REQ-env1-library). The values the compiler stores in option fields
stay in its own family: a message-valued custom option arrives as a
dynamic message over the compiler's descriptor, its own fields
readable by name, an extension within it — `proto.getExt(
proto.getExt(field.options, e.opt), e.deep)` — unreachable, the
registry's lookup finding no field of that name and the rule
failing, named as the environment's own failure to answer, rather
than a verdict.

What would reach it: the set re-decoding each file's descriptor
proto through the rebuilt family's extension types at construction,
so option values arrive in the registry's family; for a breaking
run the old side's values would need the new side's family where
the name exists, the two sets being built apart. A change set of
its own over the set's construction, taken when a rule needs the
reach.
