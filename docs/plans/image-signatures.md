# Plan: image signatures

Spec: docs/specs/provenance.md (REQ-prov-plugin-signature,
REQ-prov-unsigned-recorded), plugin-execution.md
(REQ-plugin-verify-before-run)

- [ ] 1. The signature fetched with the image — the referrers API,
      then cosign's tag convention — and judged through gitprov's
      envelope verifier as the plugin image verifier the acquirer
      already seams; require-provenance stops failing closed for
      want of a verifier
