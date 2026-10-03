# A pinned replacement's file is addressed by the replaced pair

`docs/specs/lsp.md` REQ-lsp-dependency-files addresses a dependency's
file "as the file's origin has it", by module path and version. A
pair replaced by another pair (`workspace.md` REQ-work-replace) is
read as the build list names it — the replaced path and version,
`modfiles.Load` filing the replacement's archive under the pair it
answers for — so its files are addressed `pb-module://<replaced
path>@<version>/<file>` and copied under that pair in the source
store, while the bytes are the replacement's. One address then names
different bytes across reloads that change the replacement: the
navigation index of the last compiling build holds one, the last
judgement's table another, and the content request serves the
judgement's first; a range navigation computed over the other lands
in the wrong bytes. A directory replacement has no such address (its
files are tree files).

Two defensible contracts:

1. The address names the pair the build list asks for, as now: an
   address is stable across replacement changes and spells what the
   module file requires. It does not identify bytes, and no ordering
   of the content request mends that: choosing it accepts the
   wrong-range navigation above, in the window between a
   replacement change and the next build that compiles.
2. The address names the replacement pair (`<replacement
   path>@<replacement version>`): an address identifies its bytes
   and the window is gone; the client sees the replacement's
   identity, not the requirement's, and the source-store copy lays
   out under the replacement, like the module cache does.

The tradeoff is the user's: what a client should see as a replaced
dependency's identity on the wire — the requirement, with the
window accepted, or its stand-in.

Lands: user decision.
