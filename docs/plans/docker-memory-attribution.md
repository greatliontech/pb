# Plan: a memory kill attributed from the kernel's counter

Spec: docs/specs/plugin-execution.md (REQ-plugin-resource-bounds)

- [ ] 1. The docker runner runs each plugin container under a per-run
      cgroup parent and attributes a memory kill from the parent's
      `memory.events` delta, the kernel's hierarchical count outliving
      the container's release; the daemon's event the fallback where
      the host's cgroup tree is not pb's to read (a remote daemon, a
      daemon in a VM), the record stating which source spoke; opened
      by the spike that decides the mechanism (the parent's file
      readable by pb's user after the container's scope is removed,
      the transient slice's lifetime under the systemd driver), an
      in-container static shim reading the container's own counter
      the fallback mechanism if the spike fails
- [ ] 2. The runner suite's live memory case records its kill in every
      loaded run and no longer retries a lost event
