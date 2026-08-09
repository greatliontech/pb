# Issue docs — deferred follow-ups

Tracked deferrals carrying a `Lands:` trigger. On resolution, the
load-bearing rationale is promoted inline to the spec / a test, and the
doc is deleted (git holds history).

| slug | summary | Lands |
|------|---------|-------|
| [archive-extraction-evidence-machine-local](archive-extraction-evidence-machine-local.md) | ExtractZip/writeMember oracle scratch churn keeps mutation evidence machine-local; 37 unstable-oracle survivors unattributable | gomutant scratch-namespace declaration surface, or user decision on test redesign |
| [direct-repo-resolution-caching](direct-repo-resolution-caching.md) | resolvePseudo recomputes refs, commit scan, and ancestry per call against an immutable fetched repo; memoize on Repo once the driver's access pattern exists | 13 |
| [synthesis-driver-wiring](synthesis-driver-wiring.md) | REQ-resolve-synthesis file-set/include-root clauses enforced when the driver materializes synthesized modules | 13 |
| [proxy-client-verification-wiring](proxy-client-verification-wiring.md) | REQ-proxy-client-verification enforced where the driver verifies Unverified bytes (digests, pins, provenance) | 13 |
| [contract-file-ast-walker](contract-file-ast-walker.md) | modfile/lockfile/trust triplicate the strict YAML AST walking discipline; collapse into one shared walker | user decision |
