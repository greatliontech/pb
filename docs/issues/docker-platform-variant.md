# The daemon's platform selection is variant-aware where pb's check is not

Lands: docker-runner-conformance plan chunk 3

REQ-plugin-platform-strict matches an image's platform entries on
OS and architecture, deliberately blind to the variant, and the
docker runner names that platform (`linux/arm`, say) to the daemon's
pull and create so the daemon's own default selects no other child
of the verified index (REQ-plugin-core-verifies). The daemon matches
a bare platform through containerd's normalization — `linux/arm64`
is `v8`, `linux/arm` is `v7` — so an index carrying `linux/arm/v6`
alone passes pb's check and the daemon's selection may refuse it: a
wrong refusal, with the daemon's message, of an image pb admitted.
The mechanism is stated; no 32-bit ARM daemon has exercised it, so
no fault is demonstrated and nothing is changed.
