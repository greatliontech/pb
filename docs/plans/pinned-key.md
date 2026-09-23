# Plan: the pinned-key trust tier

Spec: docs/specs/provenance.md ("Pinned keys": REQ-prov-pinned-keys-schema,
REQ-prov-pinned-key-eval, REQ-prov-pinned-key-recorded),
docs/specs/module-lockfile.md (REQ-lock-pinned-key-record), gitprov
docs/specs/verification.md ("Pinned-key signatures":
REQ-verify-signature-kind, REQ-verify-pinned-key)

- [ ] 1. gitprov: the signature's kind selected by its PEM block type,
      gitsign's CMS verified as today and every other kind, or an
      unknown one, failing closed at selection; the embedded-proof
      probe answering for a CMS signature alone
- [ ] 2. gitprov: the SSH arm — pinned OpenSSH public keys by
      fingerprint, the signature git writes verified against exactly
      them over the raw bytes, no transparency, the outcome naming the
      key's kind and fingerprint; the synthetic sigstore gains an SSH
      key pair and the builder that signs a tag with it
- [ ] 3. gitprov: the OpenPGP arm over the maintained OpenPGP fork —
      armored public keys by fingerprint, the same contract; the
      synthetic sigstore gains an OpenPGP key pair and its builder; the
      pinned-key-signatures issue closes
- [ ] 4. pb: the trust policy's `keyring` and a modules rule's `keys`
      parsed and validated — every key's fingerprint against the key,
      every rule fingerprint against the keyring, `plugins` rules
      refusing `keys` — and carried in the policy's decision
- [ ] 5. pb: the lockfile's `git-pinned-key` record read, written,
      validated and held to the downgrade guard; the fixed-point and
      fuzz grammars extended to it
- [ ] 6. pb: evaluation in the fetch client — under a rule naming keys
      the evidence verified against exactly them, naming keys
      requiring them, either evidence under keys beside identity — the
      accepted key recorded, and a pinned record re-verified on
      download against the policy's current keys, failing naming the
      key it no longer pins
- [ ] 7. pb: the update verb's reporting and the spelling of a
      pinned-key record in `dep` output; plan close-out
