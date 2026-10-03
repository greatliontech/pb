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

Resolution: the address names the pair whose bytes it serves, the
replacement's where one applies, as Go's module cache files a
replacement under its own path and gopls addresses its files there;
the requirement's name stays in the module graph (the module file),
where a name is shown, and never in a file address, which
identifies bytes; the unpinned diagnostics already name the source
pair, the one `pb dep download` pins. The source store lays the
copy out under the replacement pair; `modfiles.Module` carries
both pairs, the requirement it stands for and the source whose
bytes it holds, so no layer below the resolver reconstructs which
is which. A directory replacement is untouched: its files are tree
files.

Lands: 29.
