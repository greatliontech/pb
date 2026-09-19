# Issue docs — deferred follow-ups

Tracked deferrals carrying a `Lands:` trigger. On resolution, the
load-bearing rationale is promoted inline to the spec / a test, and the
doc is deleted (git holds history).

| slug | summary | Lands |
|------|---------|-------|
| [archive-extraction-evidence-machine-local](archive-extraction-evidence-machine-local.md) | ExtractZip/writeMember oracle scratch churn keeps mutation evidence machine-local; 37 unstable-oracle survivors unattributable | gomutant scratch-namespace declaration surface, or user decision on test redesign |
| [plugoci-oracle-socket-bound](plugoci-oracle-socket-bound.md) | acquisition mutation evidence machine-local: ocifs has no transport seam, loopback sockets disqualify oracles | ocifs transport injection or gomutant socket allowance |
| [plugrun-oracle-sandbox-bound](plugrun-oracle-sandbox-bound.md) | runner mutation evidence machine-local: kernel-sandbox oracles disqualify attribution | gomutant sandbox-oracle allowance |
| [plugrun-cgroups-live-coverage](plugrun-cgroups-live-coverage.md) | the cgroups bound mechanism's live arms run only inside a delegated cgroup subtree; the suite logs the cap and fails on demand | CI runs the runner suite under delegation with PB_TEST_REQUIRE_CGROUPS=1 |
| [mutation-campaign-freshness-cost](mutation-campaign-freshness-cost.md) | delta mutation campaigns stall in gomutant's freshness-proof preparation at ~8 GiB and never measure; the generation chunk's campaign record is outstanding | gomutant bounds or serves freshness proofs incrementally, or user decision on a larger host |
| [image-signature-verifier-home](image-signature-verifier-home.md) | offline cosign-envelope verification has no home; require-provenance plugins fail closed until it does | user decision |
| [plugrun-seam-vocabularies](plugrun-seam-vocabularies.md) | the runner seam reports tier and accounting as strings the backend already types; two vocabularies bridged by mapping functions | generation plan chunk 8 |
