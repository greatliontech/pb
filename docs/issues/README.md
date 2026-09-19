# Issue docs — deferred follow-ups

Tracked deferrals carrying a `Lands:` trigger. On resolution, the
load-bearing rationale is promoted inline to the spec / a test, and the
doc is deleted (git holds history).

| slug | summary | Lands |
|------|---------|-------|
| [archive-extraction-evidence-machine-local](archive-extraction-evidence-machine-local.md) | ExtractZip/writeMember oracle scratch churn keeps mutation evidence machine-local; 37 unstable-oracle survivors unattributable | mutation-evidence plan chunk 1 |
| [plugoci-oracle-socket-bound](plugoci-oracle-socket-bound.md) | acquisition mutation evidence machine-local: ocifs has no transport seam, loopback sockets disqualify oracles | mutation-evidence plan chunk 3 |
| [plugrun-oracle-sandbox-bound](plugrun-oracle-sandbox-bound.md) | runner mutation evidence machine-local: kernel-sandbox oracles disqualify attribution | mutation-evidence plan chunk 2 |
| [docker-oom-event-lost](docker-oom-event-lost.md) | Docker 29 over cgroup v2 loses a memory kill's record entirely under load (no flag, no event); the death is reported as one the record cannot tell apart | a Docker release recording the kill in fifty consecutive loaded runs |
| [mutation-campaign-freshness-cost](mutation-campaign-freshness-cost.md) | delta mutation campaigns stall in gomutant's freshness-proof preparation at ~8 GiB and never measure; the generation work's campaign record is outstanding | gomutant's freshness-proofs-stall-on-large-record-sets resolved |
| [image-signature-verifier-home](image-signature-verifier-home.md) | offline cosign-envelope verification has no home; require-provenance plugins fail closed until it does | gitprov's image-signatures plan, then pb's image-signatures plan chunk 1 |
| [local-scheme-darwin](local-scheme-darwin.md) | local plugins need the native runner, which exists on Linux only; macOS refuses every local entry | sandbox's rows plan chunk 2 (the darwin row) |
