# A pair replacement's module file must declare the replacement's path

`replace X: Y@v` fetches, verifies and pins Y@v under Y's own path,
and Y@v's module file must declare Y, as any fetched module's declares
the path it is fetched under (`module-file.md` REQ-modfile-identity,
workspace.md REQ-work-replace): a fork of X whose module file still
declares X is refused as an identity mismatch. Go's rule is lax here:
a replacement module's `go.mod` may declare either the replaced path
or its own, and the common fork — a repository forked on a forge, its
module file untouched — relies on it, Go's import paths being module
paths. pb's import paths are proto file paths, module-relative, so a
fork needs no declaration of X for its files to import as before;
what it needs is one line of its module file edited. The two defensible
rules, with the externally visible tradeoff:

- **Require Y's own declaration** (today's rule): an archive makes one
  claim to its name, the name it is fetched, verified and pinned
  under; a fork's author edits its module file's `module:` line before
  the fork can replace; a replacement pointing at an unedited fork
  fails at download naming the mismatch.
- **Accept either X or Y** (Go's rule, scoped to replacements): an
  unedited fork replaces X at once; an archive then carries a claim to
  a name it is not stored under, the pair of names the identity audit
  walks for — the resolver's `CheckIdentity` admitting X for a
  replacement's archive, the module file's declared path thereafter a
  graph name beside the archive's content name.

Lands: user decision.
