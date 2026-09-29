# Issue docs — deferred follow-ups

Tracked deferrals carrying a `Lands:` trigger. On resolution, the
load-bearing rationale is promoted inline to the spec / a test, and the
doc is deleted (git holds history).

| slug | summary | Lands |
|------|---------|-------|
| [mutation-record-ledger-names-old-packages](mutation-record-ledger-names-old-packages.md) | the mutation record's ledger and candidate positions keep the pre-move package names, which no retarget rewrites; the relation between a historic record and its re-measurement under the new name is lost to a reader | gomutant's retarget issue resolved with its ledger arm |
| [runner-oracle-sandbox-bound](runner-oracle-sandbox-bound.md) | runner mutation evidence machine-local: kernel-sandbox oracles disqualify attribution | gomutant's bracket admits a runner's kernel inputs: a SandboxRunner.Run mutant classified other than unstable-oracle |
| [docker-oom-event-lost](docker-oom-event-lost.md) | Docker 29 over cgroup v2 loses a memory kill's record entirely under load (no flag, no event); the death is reported as one the record cannot tell apart | the feature-full plan, chunk 13 |
| [mutation-campaign-freshness-cost](mutation-campaign-freshness-cost.md) | delta mutation campaigns stall in gomutant's freshness-proof preparation at ~8 GiB and never measure; the generation work's campaign record is outstanding | gomutant's freshness-proofs-stall-on-large-record-sets resolved |
| [local-scheme-darwin](local-scheme-darwin.md) | local plugins need the native runner, which exists on Linux only; macOS refuses every local entry | sandbox delivers a darwin row: a Start on darwin succeeds and reports a tier |
| [check-campaign-evidence-machine-local](check-campaign-evidence-machine-local.md) | the check subsystem's delta campaign banked 87 targets machine-local: three oracle tests read the filesystem root, which no bracket can fingerprint, so the 244 open mutants are unattributable until the oracles stop reading outside the tree | the campaign over the check subsystem and the repository search (`gitdir`, hoisted out of it) banks repo-committable records on the tree whose oracles read nothing outside it |
| [go-git-fork-pin](go-git-fork-pin.md) | go.mod pins a fork of go-git carrying the bounded pack parser, upstream's ending the process on a large history | an upstream go-git release carries the bounded pack parser: the replace dropped, the pin on that release |
