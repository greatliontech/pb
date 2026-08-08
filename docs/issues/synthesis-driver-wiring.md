# Synthesized-module file set and include root: driver enforcement

Lands: 13

`modfile.FromFileSet` implements REQ-resolve-synthesis's decision half:
no module file at the root ⇒ synthesized identity with no dependencies.
The requirement's other clauses — "its file set is the subtree's (per
the archive contract)" and "its include root is the subtree root" — are
term-level facts (module-archive.md: module root, file set) with no
enforcing symbol yet: they constrain how the resolution driver
materializes a synthesized module (extract the subtree as the file set;
compile with the subtree root as the include root). When the driver
assembles modules for the dep verbs, bind its wiring (or a test walking
a synthesized module end-to-end) to REQ-resolve-synthesis alongside
FromFileSet.
