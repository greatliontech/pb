# Issue docs — deferred follow-ups

Tracked deferrals carrying a `Lands:` trigger. On resolution, the
load-bearing rationale is promoted inline to the spec / a test, and the
doc is deleted (git holds history).

| slug | summary | Lands |
|------|---------|-------|
| [mutation-record-ledger-names-old-packages](mutation-record-ledger-names-old-packages.md) | the mutation record's ledger and candidate positions keep the pre-move package names, which no retarget rewrites; the relation between a historic record and its re-measurement under the new name is lost to a reader | the feature-full plan, chunk 13: closed at the campaign close-outs, the re-measurement under the new names (gomutant's retarget rewrites identity alone, by design) |
| [runner-oracle-sandbox-bound](runner-oracle-sandbox-bound.md) | runner mutation evidence machine-local: kernel-sandbox oracles disqualify attribution | the feature-full plan, chunk 18 (the kernel surfaces declared to the bracket) |
| [docker-oom-event-lost](docker-oom-event-lost.md) | Docker 29 over cgroup v2 loses a memory kill's record entirely under load (no flag, no event); the death is reported as one the record cannot tell apart | the feature-full plan, chunk 17 |
| [mutation-campaign-freshness-cost](mutation-campaign-freshness-cost.md) | delta mutation campaigns stall in gomutant's freshness-proof preparation at ~8 GiB and never measure; the generation work's campaign record is outstanding | the feature-full plan, chunk 13 (the campaign close-outs; the stall resolved upstream) |
| [local-scheme-darwin](local-scheme-darwin.md) | local plugins need the native runner, which exists on Linux only; macOS refuses every local entry | the feature-full plan, chunk 23 (after chunk 20, the sandbox's darwin row) |
| [check-campaign-evidence-machine-local](check-campaign-evidence-machine-local.md) | the check subsystem's delta campaign banked 87 targets machine-local: three oracle tests read the filesystem root, which no bracket can fingerprint, so the 244 open mutants are unattributable until the oracles stop reading outside the tree | the feature-full plan, chunk 13 (the campaign close-outs) |
| [go-git-fork-pin](go-git-fork-pin.md) | go.mod pins a fork of go-git carrying the bounded pack parser, upstream's ending the process on a large history | git-go carries pb's fetch: the go-git dependency and its replace dropped together (never proposed upstream) |
| [plugin-catalog-growth](plugin-catalog-growth.md) | three configurations on hand reference plugins the catalog does not publish (protocolbuffers/python with grpc/python, protocolbuffers/java with grpc/java, community/planetscale-vtprotobuf); each migrates only through a `--plugin` replacement until the catalog grows | the feature-full plan, chunk 14, grpc/java the last of the five (the other four at chunk 12) |
