# pb — provenance verification and trust policy

Provenance answers "who vouches for these bytes." For modules the evidence
is the origin's own gitsign-signed annotated tag, carried as a
verification pack (`module-proxy.md`) and bound to the archive through
tree-hash recomputation (`module-archive.md`); for plugin images it is a
signature over the image digest. Verification is fully offline. Acceptance
is governed by a trust policy file; verified outcomes are recorded as
lockfile provenance records (`module-lockfile.md`).

**trusted root** (term): The pinned sigstore TUF trusted-root material
(Fulcio roots and intermediates, Rekor public keys) against which all
certificate chains and transparency proofs are verified.

**verified identity** (term): The identity extracted from a valid Fulcio
certificate: the subject alternative name (SAN) and the OIDC issuer.

**trust policy** (term): The file `pb.trust.yaml` at the resolution root,
governing which provenance evidence is required and which identities are
accepted, for modules and plugin images alike.

## Verification

**REQ-prov-offline** (invariant): Provenance verification MUST complete
without network access: certificate chains verify against the trusted
root, and the Rekor inclusion proof embedded in the evidence verifies
offline against the trusted root's log keys — Rekor is never queried.

**REQ-prov-signed-tag** (behavior): `git-signed-tag` evidence MUST verify
as: the CMS signature over the raw tag object bytes validates against a
Fulcio certificate chaining to the trusted root, the embedded transparency
proof validates offline, and the verified identity is extracted from the
certificate. Evidence with no embedded transparency proof is
unverifiable and treated as absent.

**REQ-prov-tag-binding** (invariant): Accepted `git-signed-tag` evidence
MUST bind to what it vouches for: the tag names the version being
resolved, the tag references the commit in the pack, and the archive's
recomputed tree hash matches that commit's tree at the module root per
the archive contract. Evidence failing any binding step is rejected, not
ignored.

**REQ-prov-origin-consistency** (behavior): Absent an explicit identity
rule, a verified identity MUST be accepted only when it corresponds to
the module's own origin — the SAN designates the origin repository (a
maintainer identity or a CI workflow identity of that repository); an
identity from an unrelated repository or issuer is rejected.

## Trust policy

**REQ-prov-trust-schema** (wire): The trust policy file MUST contain, each
optional: `default` (`allow-unsigned`, the default, or
`require-provenance`); `modules`, a list of rules `{prefix, require,
identity: {san, issuer}}` matched against module paths; and `plugins`, a
list of rules of the same shape matched against OCI references. `san` is
a glob pattern; `issuer` is an exact string.

**REQ-prov-policy-eval** (behavior): Policy evaluation MUST apply the
rule with the longest matching prefix, falling back to `default` when no
rule matches; a subject governed by `require-provenance` (globally or by
rule) with no evidence passing verification, binding, and identity
matching fails the operation.

**REQ-prov-unsigned-recorded** (behavior): A subject resolved under
`allow-unsigned` with no accepted evidence MUST be recorded with
provenance `none` in the lockfile — tolerated, never invisible.

## Plugin images

**REQ-prov-plugin-signature** (behavior): Plugin image evidence MUST be a
sigstore signature over the image's manifest-list digest, verified
against the trusted root with the verified identity matched by the trust
policy's `plugins` rules; verification precedes any execution of the
image.
