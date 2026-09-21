# gogo/protobuf compiles from no root pb can name

Lands: user decision

`buf.build/gogo/protobuf` is served on the BSR as a hand-picked file
set — `gogoproto/gogo.proto` and the descriptor it extends — where the
repository `github.com/gogo/protobuf` holds, beside them, generator
test data whose imports (`test_proto/test.proto`,
`extension_base/extension_base.proto`, `import_public/a.proto` and
kin) resolve from no module root at all. A pb module is every file
under its root (`module-archive.md` REQ-archive-file-set), and
`gogoproto/gogo.proto` is imported by that path, so the root must be
the repository's own — which does not compile as a set. The entry is
out of the dependency table (`migrate.md` REQ-migrate-deps); a
migration naming it reports it unmapped, and `--dep` names a mirror.

The fork, the user's to weigh:

- The entry stays out: gogo's protobuf runtime is archived upstream,
  its BSR module a legacy shim, and pb ships no mirror; a migration
  naming it reports the name unmapped with the flag's form.
- greatliontech publishes a mirror holding the BSR's file set alone
  (as it publishes the rules), the entry naming the mirror: the
  migration then maps the name, at the cost of a repository pb
  maintains for an upstream that no longer moves.

The tradeoff visible outside: the first leaves every gogo user a
hand step at migration; the second adds a mirror to keep.
