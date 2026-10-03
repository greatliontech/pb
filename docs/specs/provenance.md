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
certificate chains and transparency proofs are verified — loaded from
the file named by the `trustedroot` setting: `PBTRUSTEDROOT`, or the
user configuration file's `trustedroot` key (`user-config.md`). Without
one, no evidence can verify: absent under `allow-unsigned`, a failure
under `require-provenance`; operations needing no evidence
verification run without a root.

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
MUST bind to what it vouches for, in three steps: the tag names the
version being resolved; the tag references the commit in the pack;
and the archive's recomputed tree hash matches that commit's tree at
the module root per the archive contract. The tag that names a
version is the one `module-resolution.md` REQ-resolve-release-tags
names — the subtree's own for a module declared at the tagged commit,
the repository's for one synthesized there — which the archive itself
shows by a module file at its root or none, and the module root is
the subtree whichever tag named the version. A pinned module's
record is re-verified under the subtree the record names
(`module-lockfile.md` REQ-lock-provenance-record), never the
subtree its path resolves to today: the binding is a fact about the
tag, the commit and the archive, and a redirect that moves the
module changes none of them. Evidence binds to the
one tag it carries and attests no uniqueness: that a version has one
naming tag is resolution's check at the origin. Evidence failing any
binding step is rejected, not ignored.

**REQ-prov-origin-consistency** (behavior): Absent an explicit identity
rule, a verified identity MUST be accepted only when it verifiably
designates the module's own origin: a URI SAN under the origin
repository's URL — a CI workflow identity of that repository — with the
OIDC issuer being the origin forge's CI issuer. A correspondence that
cannot be verified offline is not a designation: a maintainer's
personal identity has no derivable binding to a repository, so it is
acceptable only through an explicit identity rule; an origin on a forge
with no known CI issuer has no default identity at all. An identity
from an unrelated repository or issuer is rejected.

## Trust policy

**REQ-prov-trust-schema** (wire): The trust policy file MUST contain,
each optional: `default` (`allow-unsigned`, the default, or
`require-provenance`); `modules`, a list of rules `{prefix, require,
identity: {san, issuer}}` — and `keys`, per
REQ-prov-pinned-keys-schema — matched against module paths; and
`plugins`, a list of rules `{prefix, require, identity}`, the same
shape less `keys`, matched against the repository part of OCI
references — any `:tag` or `@digest` stripped before matching, since a
prefix scopes repositories, not versions. Rule prefixes within a list
are unique: the longest-match rule presupposes one governing rule, and
a duplicate prefix is a schema violation, not a tie to break. `san` is
a glob pattern (full-input, `/`-separated component semantics: `*` and
`?` within a component, `**` written as a complete component matching
zero or more components, character classes and alternatives, backslash
quoting); `issuer` is an exact string.

**REQ-prov-exec-policy** (wire): The trust policy file MAY additionally
contain `execution`, the resolution root's plugin-execution posture
(`plugin-execution.md`), with each key optional: `min-tier` (the sandbox
tier floor for `oci` plugins; default `Strong`), `schemes` (the
permitted identity schemes; default `[oci]` — listing `local` is the
root's explicit acceptance of unsandboxed execution), `local-pin`
(whether local binaries are content-hash pinned; `true` or `false`,
lowercase, default `true`), `plugin-overrides` (whether
invocation-scoped overrides are permitted; same spellings, default
`true`), and `limits` (plugin resource bounds overriding the
implementation defaults, each optional: `memory`, a positive integer
byte count with optional `Ki`/`Mi`/`Gi` suffix; `cpu`, a positive plain
decimal core count — digits with an optional fractional part, no signs
or exponents; `pids`, a positive integer; `timeout`, a positive plain
decimal with unit `s`, `m`, or `h`, within the representable duration
range). No other keys exist. This block is the only committed home of
these facts: generation configuration never carries them, and runner
selection — mechanism, not posture — has no committed home at all.

**REQ-prov-policy-eval** (behavior): Policy evaluation MUST apply the
rule with the longest matching prefix, falling back to `default` when no
rule matches; a subject governed by `require-provenance` (globally or by
rule) with no evidence passing verification, binding, and identity
matching fails the operation.

**REQ-prov-unsigned-recorded** (behavior): A subject resolved under
`allow-unsigned` with no accepted evidence MUST be recorded with
provenance `none` in the lockfile — tolerated, never invisible.

## Pinned keys

A trust tier between unsigned and identity-with-transparency: a rule
names the public keys it accepts for a prefix, and a git-signed tag
under that prefix verifies against those keys and no other. What the
tier proves is that a holder of a pinned key signed the tag. What it
cannot prove is when: no transparency log and no timestamp authority
binds a time to the signature, so a key stolen today signs a tag of
any date and nothing observes it, and a key's revocation or expiry
reaches pb only when the policy unpins it. A policy author choosing
this tier chooses knowing both.

**pinned key** (term): A public key a trust-policy rule accepts as a
signer for its prefix: an OpenPGP key or an SSH key, identified by its
fingerprint and carried in full in the policy, so verification looks
nothing up.

**REQ-prov-pinned-keys-schema** (wire): The trust policy file MAY
contain `keyring`, a list of pinned keys `{kind, fingerprint, key}`,
and a `modules` rule `keys`, a non-empty list of fingerprints beside
or in place of `identity`, under these rules: `kind` is `openpgp` or
`ssh`; `fingerprint` is the key's fingerprint as its kind spells it —
an OpenPGP key's the primary key's fingerprint as uppercase hex, forty
digits for a version 4 key and sixty-four for a version 6 one, an SSH
key's OpenSSH's `SHA256:` form of forty-three base64 digits; `key` is
the public key in full — an armored OpenPGP public key block holding
one key, or an OpenSSH public key line — as the verifier reads it
(gitprov's own contract), and a key whose fingerprint is not the
entry's `fingerprint` is a schema violation; a rule's fingerprint
naming no keyring entry is a schema violation, as is a fingerprint
listed twice in the keyring or in a rule; and `plugins` rules carry no
`keys`, image signatures being sigstore's alone.

**REQ-prov-pinned-key-eval** (behavior): Under a rule naming `keys`, a
subject's git-signed-tag evidence MUST verify offline against exactly
the rule's pinned keys of the signature's kind — `gitprov` verifying
the tag's OpenPGP or SSH signature, binding chain and all as for a
sigstore signature — with no transparency proof required or consulted;
with no signature by a pinned key accepted — every signature by
another key, unverifiable, or none — the operation fails, whatever
`default` or `require` says: naming keys is requiring them. A rule
naming both `keys` and `identity` accepts either evidence.

**REQ-prov-pinned-key-recorded** (behavior): A subject accepted under
a pinned key MUST be recorded in the lockfile with the key's kind and
fingerprint as the record's identity (`module-lockfile.md`,
REQ-lock-pinned-key-record); a later resolution under a policy that no
longer pins that key fails as REQ-prov-pinned-key-eval says, the
record naming the key that no longer governs.

## Plugin images

**REQ-prov-plugin-signature** (behavior): Plugin image evidence MUST be a
sigstore signature over the image's manifest-list digest — a sigstore
bundle or a simple-signing envelope, cosign's two carriers, exactly as
`gitprov` defines and verifies them — verified offline against the
trusted root with the verified identity matched by the trust policy's
`plugins` rules; verification precedes any execution of the image.

**REQ-prov-plugin-carriers** (wire): Evidence for an image MUST be
fetched from the repository the image was fetched from, at the digest
being verified, from three places, in this order: the manifest's referrers
whose artifact type is the sigstore bundle media type and whose
`dev.sigstore.bundle.predicateType` annotation names cosign's sign
predicate — the annotation read from the listing's descriptor where
the descriptor carries annotations, as the referrers API copies a
manifest's, and from the referrer's manifest where it carries none,
as the fallback tag's index names artifact types alone — each
carrying the bundle as its one layer; the manifest's
referrers whose artifact type is cosign's legacy signature
configuration media type, each layer of the simple-signing media type
an envelope with cosign's layer annotations; and the manifest under
cosign's `<algorithm>-<hex>.sig` tag, its layers envelopes the same
way. The manifest's referrers are those the registry's referrers API
lists or, where the registry has no such API, those the referrers
fallback tag `<algorithm>-<hex>` lists. Referrers are taken in digest
order, layers in manifest order, and each carrier is fetched as it is
judged, nothing past the accepted one, so the carrier recorded is a
function of the repository's content. A referrer with any other
artifact type or predicate, a layer of any other media type, an
absent tag, an empty referrers list, and a listed referrer whose
manifest the registry answers is unknown (a stale listing entry;
absence stated as for the tag) are not evidence; a carrier that is
not cosign's shape — a bundle referrer with other than one layer, a
carrier over 4 MiB — is rejected evidence; any other registry error
fetching a carrier, a blob the registry lacks under a manifest it
holds included, fails the acquisition.

**REQ-prov-plugin-classification** (behavior): Carriers MUST be judged
in discovery order until one is accepted, which is recorded. A carrier
without the material a signed time comes from is unverifiable and
treated as absent; a carrier whose verified identity the policy does
not accept is a non-acceptance; a carrier failing verification any
other way is rejected evidence and fails the acquisition under either
posture. With no carrier accepted, the image is unsigned — recorded
`none` under `allow-unsigned` (REQ-prov-unsigned-recorded), a failure
under `require-provenance` naming the reason. For a pinned image the
recorded provenance must still hold (REQ-lock-no-silent-downgrade): a
carrier the policy accepts whose record is not the pin's is passed
over while a later one may reproduce it, and with none reproducing it
the acquisition fails as a record the evidence no longer bears, under
either posture.

**REQ-prov-plugin-identity** (behavior): A plugin image has no default
identity: no offline-verifiable correspondence binds a repository to
a signer, as REQ-prov-origin-consistency requires of a designation, so
evidence MUST be accepted only through an explicit identity rule. With no
identity rule governing the reference, or no trusted root configured,
no evidence is judged and the image is unsigned as above.

**REQ-prov-plugin-evidence-kept** (behavior): Evidence fetched for an
image MUST be kept with pb's plugin content, keyed by the digest, as
the carriers taken in discovery order; a later acquisition judges the
kept evidence first and fetches only when it holds none the judgement
accepts — under the identity rule and, for a pinned image, the
recorded provenance — what the fetch takes then replacing it, so an
acquisition whose kept evidence is accepted makes no round trip, and
a signature since removed from the registry goes unnoticed while the
kept one is accepted. Kept evidence carries no authority: it is
judged exactly as fetched evidence is, kept evidence that cannot be
read is absent, never an error, and a keep that cannot be written
changes no outcome. A fetch that takes no carrier keeps nothing, so
an unsigned image is asked for again on every acquisition, and a
fetch that rejects keeps nothing. An explicit update of the pin
(`dep-verbs.md`) fetches anew without judging what was kept, and
keeps what it takes.

**REQ-prov-plugin-evidence-store** (wire): Kept evidence MUST live at
`<user cache>/pb/plugin-evidence/<algorithm>/<hex>.json` for the
digest `<algorithm>:<hex>`: a JSON object whose `carriers` list holds,
in discovery order, objects of `where`, the carrier's origin as
discovery names it, and one of `bundle`, the sigstore bundle's bytes
as fetched, base64, or `envelope`, the simple-signing envelope's parts —
`payload` base64, `signature`, `certificate`, `chain`, `rekorBundle`,
`rfc3161Timestamp` as annotated, absent ones omitted. An entry is at
most 64 MiB, a larger one being absent. Entries are written
atomically and whole; apart from the transient temporary files atomic
writes leave on interruption — never read as entries — no other
content lives under the store. An entry persists until the store is
emptied (`dep-verbs.md` REQ-dep-clean): it may outlive the image it
vouches for, kilobytes carrying no authority.
