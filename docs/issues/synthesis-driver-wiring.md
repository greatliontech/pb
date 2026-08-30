# Synthesized-module include root: generation enforcement

Lands: 2 (generation plan — compilation over the resolution driver)

REQ-resolve-synthesis's file-set clause is enforced end to end: the
archive constructed for a synthesized subtree is the subtree's file set
(direct-source construction) and the resolution pipeline consumes a
module-file-less archive as a synthesized module (identity from the
required path, no dependencies). The remaining clause — "its include
root is the subtree root" — constrains how compilation materializes a
synthesized module's files and has no enforcing symbol until the
generation subsystem compiles modules; bind its wiring (or a
compile-through test) to REQ-resolve-synthesis then.
