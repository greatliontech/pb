# A fork cannot stand in for a dependency's dependency

Lands: user decision — a replace directive naming another module
path only, every input still fetched and verified under the pin
store and the trust policy; or one that may also name a directory in
the working tree, the workspace's own trust extended to a
dependency's content

Depending on a fork works where the workspace itself requires the
module: it requires the fork's path instead, and the fork's files
keep their import paths. It does not work where a dependency
requires the original: the fork and the original both provide the
same paths, and the compile refuses the ambiguity (generation.md
REQ-gen-compile). Go covers the case with `replace`; pb has no such
directive, so the go-mod pattern of forking a dependency is
complete only at the first level of the graph.

A replace directive would live in the workspace file, the one file
that is the workspace's own and never travels in an archive
(workspace.md), and would say that one module path stands for
another throughout the build list. Two shapes are defensible. Naming
another module path only keeps every input on the same footing:
the fork is fetched, verified and pinned as any module, and the
trust policy judges it under its own path. Naming a directory in the
working tree besides admits the working tree's trust for a
dependency's content, which serves development on a patch before it
is published, as Go's local replace does, and is the one input a
build would take on presence beyond the workspace's own files. The
questions either shape settles the same way: tidy reads the graph
through the replacement; the resolution root's own module files
never carry it; a workspace holding one publishes its modules as it
does today, the replacement a working arrangement.
