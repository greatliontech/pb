# Consumer bootstrap and partial migration recovery guidance

Lands: when adopter setup documentation replaces the README's `wip` placeholder,
or migrate's partial-output recovery guidance is next changed.

## Observed consumer path

Weaver's first adoption uses a buf v2 module at `proto`, the STANDARD lint and
FILE breaking selections, and local `protoc-gen-go` / `protoc-gen-go-grpc` plugins.
The installed pb's `migrate` maps these facts and writes the expected files.

On a machine where Git can access the private `greatliontech/buf-rules` repository
through its credential helper, pb's independent HTTPS authentication has no netrc
entry. Migration reports no discovered ruleset version, writes `pb.lint.yaml`
without a version, and fails its final tidy. The report suggests a `--dep`
replacement for migration, but rerunning migration in place is refused because
the generated pb files already exist. `pb dep update
github.com/greatliontech/buf-rules` also refuses the versionless non-workspace
ruleset before resolving an update.

The supported recovery was to add the ruleset's `version: v0.2.0` in the generated
lint file, provide credentials through `PBNETRC`, and rerun `pb dep tidy`.
SSH routing is another documented choice, but this machine's inherited agent
socket was stale; pb correctly named that separate failure.

The subsequent `pb generate` refused the local plugin scheme until the project
explicitly supplied:

```yaml
execution:
  schemes:
    - local
```

in `pb.trust.yaml`. With that explicit opt-in, generation was byte-for-byte equal
to Weaver's checked-in Go output and `pb lint`, `pb build`, and `pb dep verify`
passed. Local binary hashes were pinned on first use.

## Gap and requested resolution

These refusals follow the current authentication, migration, and trust contracts;
this report does not claim credentials should be inherited silently or migration
should authorize unsandboxed plugins. The consumer-facing gap is that the README
is only `wip`, command help gives no bootstrap path, and recovery requires finding
the relevant portions of several internal specs.

Provide a short adopter guide linked from the README covering:

- Authentication differences from Git credential helpers, with the supported
  netrc and SSH-agent configuration and no credentials in project files.
- The files retained after partial migration, how to repair a missing ruleset
  version and continue with tidy, and when migration can instead be rerun safely.
- The separate, explicit local-plugin trust opt-in and first-use binary pinning.
- A complete migrate/tidy/generate/lint/build/verify example and expected outcomes.

Consider making migration's partial-failure diagnostic point at the repair path
rather than suggesting only a fresh migration flag. Whether the tool should offer
an additional repair verb is a design choice, not a requirement of this report.

References: `docs/specs/migrate.md` REQ-migrate-verb/report,
`docs/specs/module-resolution.md` Private origins, `docs/specs/user-config.md`,
`docs/specs/provenance.md` REQ-prov-exec-policy, and
`docs/specs/plugin-execution.md` Local binaries.
