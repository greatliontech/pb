# Pinned-key provenance: the tier between unsigned and identity

Lands: the pinned-key plan opened, this doc its seed

The provenance spec's pinned-key tier is stated (provenance.md
"Pinned keys", module-lockfile.md REQ-lock-pinned-key-record) and
not built. The plan that builds it, in order:

1. gitprov: the signature's kind selected by its PEM block type —
   gitsign's CMS as today, OpenPGP and SSH signatures beside it —
   and verification of the two against caller-pinned keys, offline,
   no transparency (its verification.md, the requirements marked as
   landing with this plan). Two verifiers enter its dependency graph
   with the user's yes: the maintained OpenPGP fork, since x/crypto's
   is frozen and carries the open advisory pb's vulnerability check
   reports, and x/crypto's ssh package for SSH signatures.
2. pb: the trust policy's `keyring` and a rule's `keys`, parsed and
   validated (fingerprint against key); evaluation under a rule
   naming keys, requiring by construction; the lockfile's
   `git-pinned-key` record; the fetch client's evidence path taking a
   pinned-key verdict beside a sigstore one.
3. The same over the update verb's reporting and the spelling of
   provenance in `dep` output.

Not in the plan: pinned keys for plugin images (sigstore's alone), a
key fetched from anywhere (the policy carries every key in full).
