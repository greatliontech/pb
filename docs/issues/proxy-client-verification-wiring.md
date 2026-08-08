# Client verification: driver enforcement over Unverified bytes

Lands: 13

REQ-proxy-client-verification is an invariant on the whole client: no
artifact accepted on transport trust. This change set contributes the
seam — `proxy.Unverified` marks every fetched byte as vouched-for by
nothing, and `proxy.Get`/`proxy.Fallthrough` return only that type — but
the verification calls themselves (archive digest recomputation per
module-archive.md, pin matching per module-lockfile.md, provenance
checks before use) are wired where the driver consumes artifacts. When
the dep verbs assemble the fetch-verify pipeline, bind the driver's
verification path (or an end-to-end test proving a tampered artifact is
rejected from every source kind) to REQ-proxy-client-verification.
