# Plan: the build verb

Spec: docs/specs/build.md; docs/specs/generation.md (REQ-gen-request,
REQ-gen-request-determinism)

- [x] 1. compile, genrequest: one topological walk over a compiled
      result — the closure that walk filtered of the well-known
      imports, pinned equal by the closure property; the plugin
      request built over the order the compile hands it; the
      descriptor set assembled from it fresh per call, every
      reachable file's descriptor as the request carries it
- [x] 2. atomicfile: a file lands at the mode asked for — 0644 less
      the umask, created so rather than changed to — for every writer:
      the module
      cache, the lockfile and module files, generated files, the
      migration's outputs, the evidence store; pinned on a real
      filesystem
- [ ] 3. dep, cmd: the verb `pb build <file>` — the set serialized
      deterministically, written whole through the atomic file
      writer, a directory or a `.proto` path refused, the one-line
      report; plan close-out
