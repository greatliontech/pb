# Plan: the package structure by spec domain

Spec: every spec under docs/specs names a domain; the layering is the
domains' order, enforced in code (internal/README.md and the layering
test beside it, chunk 1).

- [x] 1. The layering invariant: internal/README.md names the domains
      and their order; one layering test at internal/ proves every
      import edge among pb's packages points down that order, over
      today's packages, subsuming the test-support layering test; the
      stale yamlshape mutation records pruned
- [x] 2. module: archive, modpath, version, modfile, mvs, workspace,
      lockfile under internal/module, the path rule as the root
      package; bindings and records retargeted
- [x] 3. provenance: the tag verifier as the root package, imagesig as
      image with discovery and the evidence store its subpackages, the
      judge's no-network allowlist kept narrow, trust beside them
- [x] 4. source: proxy, origin, direct, modfetch under internal/source
      as proxy, origin, direct, fetch, the HTTPS posture as the root
      package
- [x] 5. proto: modfiles, protocomp, protoimport under internal/proto
      as modfiles, compile, importcheck (files and imports being the
      commonest locals in their importers)
- [x] 6. plugin: plugexec as the root vocabulary, pluglocal, plugoci,
      plugrun, genfile, genrequest under internal/plugin as local, oci,
      runner (run and runner being locals in the importers; the
      locals named runner become run), genfile, genrequest
- [x] 7. The driver and the verbs: resolve and dep against the final
      layering, the command's assembly with them; the CI paths, the
      issue cites, the README of internal/testing; the host platform
      one type on the plugin root (host-platform-spelled-three-ways)
- [x] 8. One acquirer seam: a scheme-tagged Acquired in a subpackage
      of the plugin domain, one dep.Acquirer keyed by scheme
      (two-acquirer-shapes)
- [ ] 9. The acquisition result on the domain root, the runner's spec
      taking the image facts as that type
      (acquisition-result-on-the-root); plan close-out
