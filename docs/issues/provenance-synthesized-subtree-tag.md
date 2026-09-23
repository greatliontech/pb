# A synthesized subtree's provenance binds to the wrong tag and tree

Lands: provenance.md's subject term naming the tag namespace apart
from the module root — the tag a synthesized subtree's release is
bound to being the repository's, the tree the subtree's at that
commit

A synthesized subtree module takes its releases from the
repository's tags (module-resolution.md REQ-resolve-release-tags):
`github.com/envoyproxy/envoy/api@v1.30.0` is the commit `v1.30.0`
names, its archive the `api` subtree there. The verification pack
the direct source renders for it and the subject the client verifies
evidence against both spell the module's subtree as the tag's
namespace: the pack looks the tag up under `api/v1.30.0`, which does
not exist, and where it did the pack's tree path would walk the
repository root rather than the subtree (direct.Repo.VerificationPack
takes one subtree for both the tag's namespace and the tree to walk);
the client's subject (fetch.verifyEvidence, provenance.Subject) names
the tag `api/v1.30.0` (REQ-prov-tag-binding), so a genuine signed
repository release consumed as a synthesized subtree verifies against
a tag that names no such version and is rejected as tampering, never
read as absent. No test covers provenance for a synthesized subtree.
The fix wants the provenance contract to say which tag a synthesized
module's version is bound to — the repository's, the one that named
the version — with the tree binding walking to the subtree at that
commit: the pack and the subject carrying the namespace and the
module root as two things, as resolution now judges them.
