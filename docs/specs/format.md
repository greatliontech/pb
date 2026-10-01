# pb — format

`pb format` rewrites the workspace's protobuf files into one canonical
form: a fixed style with no options, buf's, so a file formatted by
either tool reads the same. The form is a pure function of the file's
tokens and comments: two files with the same tokens and comments in
the same places format to the same bytes, whatever whitespace they
were written with. The verb reads the files pb's own checks read
(`check-rules.md`), never a dependency's.

**canonical form** (term): The one spelling of a parsable protobuf
file this document defines: its tokens in the file's own order
except where REQ-format-header and REQ-format-normalization reorder
or drop them, laid out by REQ-format-layout, its comments placed by
REQ-format-comments.

**own file** (term): A file of a workspace module's file set
(`module-archive.md` REQ-archive-file-set) whose name ends in
`.proto` — a workspace copy of a well-known file among them, no file
of the build but a file of the tree — named by its path from the
workspace root.

**corpus** (term): The pairs of files under
`internal/format/testdata/buf`, each a `.proto` input beside the
`.golden` canonical form of it, copied from buf's own formatter
tests under their Apache-2.0 license (the `LICENSE` beside them, the
commit named in the directory's `README`). The corpus is the
conformance anchor of this document: a rule below is read as the
corpus reads it, and a pair the implementation formats otherwise is a
fault of the implementation or of this document, never of the pair.

## The verb

**REQ-format-verb** (behavior): `pb format` MUST read every own file
of the workspace, compute its canonical form, and act on the files
whose bytes differ from it — the *unformatted* files — as its flags
say, taking `--diff`, `--write` and `--exit-code`, each a boolean,
freely combined, and no argument: without `--write` it writes
nothing; with `--write` it rewrites each unformatted file in place,
byte for byte the canonical form, atomically and with the file's
mode kept, other files untouched — a symbolic link among the
unformatted files fails the run before anything is written, naming
it, the link being no file the verb replaces; without
`--diff` it prints each unformatted file's path on standard output,
one per line, in path order; with `--diff` it prints instead, per
unformatted file in path order, a unified diff from the file as it is
to its canonical form, headed `--- <path>` and `+++ <path>`, hunks
with three lines of context, each hunk `@@ -<start>,<count>
+<start>,<count> @@`, a file whose last line ends without a newline
noted `\ No newline at end of file` after that line; it exits 0 where
every own file is formatted, or where some are not and `--exit-code`
is absent, and 1, nothing more said, where some are not and
`--exit-code` is given — the rewriting under `--write` counted as the
files were before it, so `--write --exit-code` reports what the run
changed; and it fails as every verb fails, exit status 1 with the
cause on standard error: naming the file and the parse error where
an own file does not parse as protobuf, every file read and judged
before any is written, so nothing is written; or naming the cause
where a file cannot be written, the files before it in path order
rewritten and reported, those after it untouched. A workspace with no
own file prints nothing and exits 0. The paths printed and diffed are
the own files' paths from the workspace root. The verb formats the
files as text: a file that parses is rewritten whatever its
semantics, an unresolvable type or a duplicate name no concern of the
formatter's.

**REQ-format-pure** (invariant): The canonical form MUST be a pure
function of the file's token sequence and of its comments, each
comment's text and its attachment (REQ-format-comments): whitespace
between tokens contributes nothing but the blank-line facts
REQ-format-layout names and the presence of whitespace beside a
comment that REQ-format-comments reads, and a file's path, its
module, and every other file contribute nothing.

**REQ-format-idempotent** (invariant): Formatting MUST be idempotent:
the canonical form of a canonical form is itself, byte for byte.

**REQ-format-tokens** (invariant): The canonical form MUST carry
exactly the file's tokens, in order, each spelled as written — a
string literal with its own quotes and escapes, a number in its own
base and digits, an identifier as written — and exactly the file's
comments, each with its text (reflowed only as REQ-format-comments
says), except what REQ-format-normalization and REQ-format-header
name: the dropped optional separators and empty declarations (a
bare `;`, which carries no blank-line fact), the dropped duplicate
imports, `<` and `>` of a message literal written `{` and `}`, a
message field's omitted `:` written, a byte-order mark opening the
file dropped, and the header's reordering. Nothing else is added, dropped or reordered, so the
formatted file compiles to the same descriptor as the file it came
from, its imports and file options in the canonical order — its
source positions aside, and a comment moved off a dropped token
attaching, in the descriptor's source info, to the token it was
moved to.

## Normalization

**REQ-format-header** (behavior): A file's header — its `edition` or
`syntax` statement, its `package` statement, its imports and its
file-level options — MUST be written first, in that order, whatever
order the file declares them in, each declaration carrying its own
comments with it (REQ-format-comments); the declarations that are
none of these follow in the file's order. The imports are sorted by
path, byte order, a `public` import before a plain one before a
`weak` one of the same path, and among equal paths and kinds an
import carrying a comment before one carrying none; an import whose
path equals the previous written import's and which carries no
comment is dropped. The file-level options are sorted with every
standard option (a name not opening with `(`) before every custom
option (one opening with `(`), each group by the option name's
spelling in byte order, the name spelled as its parts joined by `.`,
a custom part in its parentheses. A blank line separates the
package from the imports, the imports from the options, and the
header from what follows, each present, and the statement from the
package where the source had one before the package; the imports are
written each on its own line with no blank line among them, and so
are the options, their own blank lines dropped (a blank line inside
the leading comments of the first import or the first option is
kept, and then stands for the separating one). A file with no header
writes nothing before its first declaration.

**REQ-format-normalization** (behavior): The canonical form MUST
spell: an rpc with no body as `rpc Name(In) returns (Out);`, one with
a body or with a comment inside its braces with braces; an empty body
— a message, enum, service, oneof, extend, group, rpc, message
literal, array literal or compact options with no element and no
comment inside its delimiters — as the two delimiters with nothing
between, `{}`, `[]`; a message literal's elements without the optional
`,` or `;` separators, the comments of a dropped separator — its
leading ones, then its trailing ones — carried to the element's
value as trailing comments after the value's own; a message
literal's field name always followed by `:`, written where the file
omitted it; a message literal delimited by `<` and `>` with `{` and
`}`; compact options as `[name = value]` on the field's line where
there is one option and no comment stands after the `[` or before
the name — a compound string value written one token per line after
the `=`, the `]` directly after the last token — and otherwise one
option per line, `,` after each but the last, between `[` and `]` on
lines of their own; an array literal as `[value]` where there is one element,
no message or array literal, and no comment inside the brackets, and
otherwise one element per line, `,` after each but the last; a message
literal as `{name: value}` where there is at most one element, that
element's value no message or array literal and no compound string,
and no comment inside the braces, and otherwise one element per
line; a compound string
literal — several adjacent string tokens — one token per line,
indented one level under its option or field; a range as `1 to 10`,
`100 to max`; a reserved list and an extension range list with `, `
after each entry but the last; a map type as `map<string, int32>`; an
rpc's types as `(stream Name)`; a field as `[label ]type name = tag[
options];`, a group as `[label ]group Name = tag[ options] {`; an
option as `option name = value;`; each statement's `;` directly after
its last token.

## Layout

**REQ-format-layout** (wire): The canonical form MUST be laid out as
follows. Lines end in `\n` alone, the last line too, a file writing
nothing ending without one. Indentation is two spaces per level: a
body's lines one level deeper than the line opening it, a closing
delimiter at the opener's level, a compound string's tokens and a
multi-line option list's entries one level deeper than the line they
belong to. No line ends in a space or a tab. Tokens of one line are
separated by one space, except that no space stands after `(`, `[`,
`{` or `<` or before `)`, `]`, `}`, `>`, `,`, `;` or `:` of a message
field, around the `.` of a qualified name or the `/` of a type URL,
between an rpc's name and its `(`, between a map's `map` and its `<`,
or between a sign and its number; an in-line comment between two
tokens is spaced as REQ-format-comments says, whatever the tokens
(`] /* c */ : "v"`), and a comment between a sign and its number
keeps a space on each side. A declaration opens on a
line of its own, its leading comments on the lines above it. Blank
lines: at most one in a row anywhere; one between two declarations
of a body or of the file where the source had one or more between
them (between the last token or comment of the one and the first
comment or token of the other), none where it had none — except that
a declaration of the file carrying a leading comment is separated
from what precedes it by one where the source had none anywhere
before it, among its comments or between them and the declaration,
as the header is from the first declaration; none before the first declaration of a body or of
the file, none after the last before the closing delimiter; none at
the start of the file. A
multi-line option list, array literal or message literal is laid out
as a body: its opener ends the line, its entries each on a line, its
closer opens the next line at the owner's level; a closer followed by
more of the statement (`];`, `},`, `}];`) continues on its line.

**REQ-format-comments** (wire): Every comment of the file MUST be
written, attached to the token the parser attaches it to: a comment
beginning on the line of the token before it, with no token after
it on the line it ends on, is that token's *trailing* comment; every
other comment is the *leading* comment of the next token; a comment
attached to an empty declaration's `;` is the leading comment of the
next declaration of its body, or of the body's closing delimiter, or
of the end of the file. A leading comment of a
token that opens a line — a declaration's first token, a list entry's
first token, a body's closer — is written on lines of its own above
the token, at the token's indentation, each `//` comment on one line
and a block comment as REQ-format-block-comment lays it out, a blank
line kept before a comment where the source had one before it (and
before the token where the source had one between the last comment
and the token), a blank line never written before the first comment
of a body's first declaration. A leading comment of any other token
is written in line before it, on the token's line, as a block
comment: a `//` comment becomes `/* text */`, its text trimmed; a
block comment spanning lines is joined on one line, each line
trimmed, the lines separated by one space; a space separates the
comment from the token where the source had whitespace between them,
none otherwise, and a space precedes the comment where the source
had whitespace before it or where `;` or `}` precedes it. A `//`
comment whose text holds `*/` can be no block comment: in line, it
is written as it is and ends its line, the rest of the statement
continuing on the next line at the line's indentation, deeper by the
delimiters the line opened. A trailing comment of the token ending a line — a
statement's `;`, a body's opener or closer, a list entry's `,` or
last token — is written after it on its line, separated by one
space, as it is: `//` comments as `//` comments, block comments
spanning lines as REQ-format-block-comment lays them out; a trailing
comment of any other token is written in line after it as a block
comment, as for leading comments. A comment attached to a token the
canonical form drops — a message literal's separator, a duplicate
import — is kept: the separator's comments become the element's
value's trailing comments after its own (REQ-format-normalization),
and a duplicate import carrying a comment is never dropped. A token
carries one trailing comment on its line, as the parser attaches it;
where more are carried to it, the first ends the line and the rest
follow on lines of their own at the level of the line that follows,
where the parser reads them as its leading comments. The closing
delimiter of a message-literal element's literal value keeps its one
trailing comment in line, as a block comment. A comment
before the end of the file, after its last token, is written on lines
of its own after the last declaration, a blank line before it where
the source had one. Comment text is written as it was, byte for byte,
but for the trimming and joining this requirement and
REQ-format-block-comment name.

**REQ-format-block-comment** (wire): A block comment spanning several
lines, written on lines of its own or trailing the token ending a
line, MUST be re-indented: its first line `/*` and what follows it on
that line, trimmed of trailing space; its last line's `*/` on a line
of its own where the source had it so, at the comment's indentation —
one space further where every interior line opens with `*`; each
interior line trimmed of trailing space, its leading whitespace
dropped and replaced by the comment's indentation followed by three
spaces, or by one space where every interior line opens with the same
non-alphanumeric character (`*`, `|`, `=`, ...), that character kept;
tabs in the leading whitespace counted to the next multiple of eight
in telling which line is least indented; a blank interior line
written empty. A `//` comment is written trimmed, the `//` kept.

## Conformance

**REQ-format-corpus** (behavior): Every pair of the corpus MUST format
to its golden byte for byte, and every golden to itself; the corpus
grows by pb's own pairs beside buf's — each a `.proto` and its
`.golden` under `internal/format/testdata/pb` — for a form the
corpus does not exercise, a pb pair pinning a rule above exactly as
the rule states it.
