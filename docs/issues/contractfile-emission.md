# The contract files' emitters share no rendering helper

Lands: the migrate plan's chunk 6

`internal/contractfile` owns the contract files' reader, the
admissibility rule over YAML constructs, the promise the package doc
states — a contract file reads identically under every consumer, so
a canonical emission never produces an anchor, an alias, a tag or a
merge key — and, since the lint file's emission landed, the scalar
rule that keeps it (`Spell`: plain only where the reader reads the
spelling back as that text and no YAML schema of any version types
it, double-quoted otherwise), which the lint file's emitter
(`internal/check/lintfile` `Encode`) and the workspace file's
(`internal/module/workspace` `Encode`) apply. The rest is not shared.
The lint file's emitter alone carries a write-time identity guard
comparing the rendering's reading to the file given; the module
file's emitter (`internal/module/modfile` `Encode`) writes its values
raw, sound because its domains are validated — a module path and a
`v`-prefixed version are never typed by a resolver — and unstated
where they widen; the lock file's writer is a fourth, on its own
typed encoder. Each spells indentation, lists and mappings by hand.

The collapse: a document-level identity check moves into
`contractfile` beside `Spell` and the reader whose grammar they hold
to, and the emitters build on one indentation, list and mapping
helper applying `Spell` to every scalar, so a value is spelled one
way across the files and the promise the package doc makes is the
emitters' by construction rather than by each one's care. Invariants
preserved: each file's key order and list orders as its spec pins
them (`module-file.md` REQ-modfile-emission, `workspace.md`
REQ-work-emission, `check-rules.md` REQ-lint-emission, the lock
file's clause), and the lint file's nil-and-empty distinction for
`enable`. Two things the fold meets rather than discovers: a module
path whose first host label is all digits (`123.com/x`, legal) is
quoted by `Spell`'s digit rule, so existing module files re-spell
their `module` value and `deps` keys with no change of reading; and
the module file's `deps` entries are key positions, which `Spell`'s
list-item probe does not certify alone, so the module writer needs
the identity guard the collapse carries, not `Spell` by itself. The
generation
file's emitter, the migration's fourth in chunk 6, is built on the
shared helper rather than as a fifth copy.
