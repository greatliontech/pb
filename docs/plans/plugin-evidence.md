# Plan: plugin evidence kept and pins moved

Spec: docs/specs/provenance.md (REQ-prov-plugin-carriers,
REQ-prov-plugin-classification), docs/specs/plugin-execution.md
(REQ-plugin-verify-before-run), docs/specs/module-lockfile.md
(REQ-lock-no-silent-downgrade), docs/specs/dep-verbs.md
(REQ-dep-update)

- [ ] 1. A governed plugin's signature evidence kept beside its
      content in the plugin store and judged from there on later
      acquisitions, fetched again only when the store holds none the
      policy accepts, so a build with a warm store and an unchanged
      pin runs offline
- [ ] 2. `pb dep update` extended to plugin pins: a named reference
      re-resolved and re-judged, its digest and record rewritten
      under the explicit-update sanction
