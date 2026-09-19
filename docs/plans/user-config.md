# Plan: user configuration

Spec: docs/specs/user-config.md; plugin-execution.md
(REQ-plugin-runner-selection, REQ-plugin-core-verifies),
module-proxy.md (REQ-proxy-config), dep-verbs.md (the module cache
term), provenance.md (the trusted root term)

- [x] 1. The file and its loader: location, the strict flat format,
      the five existing settings resolved flag over environment over
      file over default through one resolver the CLI wires for the
      runner, the proxy, the cache and the trusted root
- [ ] 2. The plugin byte path: the `plugin-pull` / `PBPLUGINPULL`
      setting joins the setting term and REQ-plugin-core-verifies;
      set to `docker` it makes the docker runner have the daemon pull
      the verified digest instead of importing pb's export; the record
      checks unchanged; the native runner refuses the setting
