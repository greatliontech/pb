# Image-signature verification: no offline verifier exists yet

Lands: gitprov's image-signatures plan (offline envelope
verification against the pinned root, gitprov owning that
verification already) and pb's image-signatures plan chunk 1 (the
signature fetched by referrers and judged through it)

REQ-prov-plugin-signature needs offline verification of a sigstore
signature over a plugin image's manifest-list digest, against pb's
pinned trusted root. No component provides it: gitprov verifies git
objects only (CMS over commit/tag bytes); ociplug's internal/verify is
sigstore-go/TUF-online and ociplug-internal; pb must not grow cosign's
dependency tree.

Until a verifier exists, plugin acquisition is spec-conformant for the
default posture: unsigned images are tolerated and recorded as
provenance none (REQ-prov-unsigned-recorded), and a plugins rule or
default of require-provenance fails closed naming the missing
verifier. Nothing silently passes.

The verifier's home is gitprov, which owns offline sigstore
verification against a pinned root and already carries cosign's
offline transparency-log machinery in its graph: its image-signatures
plan adds the envelope verifier beside the git-object one, and pb's
image-signatures plan wires it into the acquirer's seam.
