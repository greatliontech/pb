# Image-signature verification: no offline verifier exists yet

Lands: user decision

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

Candidate homes, in the order the ecosystem's precedents suggest:
gitprov (owns "offline sigstore verification against a pinned root";
cosign/v3's offline tlog machinery is already in its graph), or the
standalone validate module sketched when ociplug's verify package is
extracted. Cross-repo scheduling is the user's call.
