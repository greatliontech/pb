# Plan: docker runner conformance

Spec: docs/specs/plugin-execution.md (REQ-plugin-sandboxed,
REQ-plugin-runner-independence, REQ-plugin-resource-bounds,
REQ-plugin-min-tier, REQ-plugin-platform-strict,
REQ-plugin-core-verifies)

- [x] 1. The docker runner's known deviations named: the daemon's
      management mounts and injected PATH, and the image's own
      configuration binding on a daemon image, as REQ-plugin-sandboxed's
      stated exceptions and REQ-plugin-runner-independence's scope;
      the refused-fork attribution qualifier and the floor's bottom
      row stated where the code already holds them
- [ ] 2. The memory kill's second record: a 137 exit the daemon's
      record leaves unattributed is read against the daemon's OOM
      event for the container before its release
- [ ] 3. The admitted child named to the daemon: the seam records the
      platform of the manifest-list entry it admitted, the docker
      runner pulls and creates for exactly that platform, and an
      entry set ambiguous for the host (several variants) is refused
      as the store's own rule refuses it
- [ ] 4. Continuous integration runs the runner suite under a
      delegated cgroup subtree with the cgroups and docker live arms
      demanded
