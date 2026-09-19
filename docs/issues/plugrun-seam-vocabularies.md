# The runner seam carries two vocabularies the backend already types

Lands: generation plan chunk 8 (the docker runner is the seam's second
producer; one typed report for both is decided there)

`plugrun.Result` reports the tier and the bounds accounting as
strings: `Tier` in plugexec's vocabulary (`TierNone` .. `TierStrong`,
ranked by `TierRank`/`TierBelow`/`ValidTier`) and `Bounds` as
`BoundsCgroups`/`BoundsRlimits`, string copies of
`sandbox.Accounting.String()`. The sandbox types both
(`sandbox.Isolation`, `sandbox.Accounting`); the native runner bridges
with `tierOf`/`isolationOf`, and a test pins the accounting names
equal. Two code concepts where the spec has one ("sandbox tier"; the
accounting that enforced the bounds).

Collapse sketch: the seam's `Result` carries a typed tier and a typed
accounting whose vocabulary is the seam's own (the docker runner
derives them from the daemon's record, not from sandbox), plugexec's
string tiers become that type's names for the trust policy's
serialized form, and the runner-side bridges and the pinned equality
go. Invariants preserved: the tier judged is the tier reported
(REQ-plugin-reported-tier); an unknown value never ranks as
acceptable.
