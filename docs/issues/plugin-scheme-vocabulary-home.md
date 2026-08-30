# Plugin scheme/tier vocabulary: two homes, soon three

Lands: 4 (generation plan — the plugin package created for OCI acquisition becomes the vocabulary home)

`trust.SchemeOCI/SchemeLocal` and `lockfile.SchemeOCI/SchemeLocal` are
two homes for the same closed vocabulary (plugin-execution.md's
identity schemes), and `trust` also carries the sandbox tier names.
Neither package can naturally import the other today, and the
execution subsystem will want both vocabularies.

Collapse sketch: when the plugin-execution package lands, it becomes
the vocabulary's one home (schemes and tiers); `trust` and `lockfile`
import it and their local constants are deleted. Invariant preserved:
the string values are wire facts pinned by both specs' schemas, so the
collapse moves declarations only.
