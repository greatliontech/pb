# pb — the language server

`pb lsp` serves an editor over the Language Server Protocol: the
diagnostics pb's own compile and lint produce, navigation and hover
over the resolved build's descriptors, and formatting into the
canonical form. It is the verbs' engines behind a protocol, never a
second judgement: what the server says of a file is what `pb lint`
and `pb format` say of it, given the same tree.

**server** (term): The process `pb lsp` starts, speaking the Language
Server Protocol (version 3.18) over its standard input and output,
one connection, Content-Length framed, for one client and one
resolution root. It ends with the connection, or on `exit`.

**client root** (term): The directory the client names — its first
workspace folder, every other folder ignored, else its root URI;
none names no directory. It is read as the working directory a verb
runs in: the resolution root governing it (`workspace.md`: the
nearest workspace file above it, else the module it lies in) is the
server's session, loaded as the verbs load theirs; a directory no
resolution root governs loads no session.

**document** (term): A file the client has opened
(`textDocument/didOpen`) whose contents the server holds at the
client's version until it is closed.

**build file** (term): A file the resolved build reads from the
working tree: an own file (`format.md`: a workspace module's `.proto`
file, named by its path from the resolution root's directory), or a
`.proto` file of a directory replacement (`workspace.md`
REQ-work-replace-dir), which is compiled with the build and checked
by no rule, as the verbs have it. A workspace copy of a file of the
well-known imports (`module-resolution.md`) is an own file and no
build file: the build reads the toolchain's.

**outside the build** (term): A `.proto` document that is no build
file, for one of these reasons and no other, the first that applies
the one named: no build is loaded; it is a dependency's file read
through REQ-lsp-dependency-files; the file lies under no resolution
root, or under another than the server's; it lies in no workspace
module and no directory replacement; it is a workspace copy of a
file of the well-known imports.

**build home** (term): The file a judgement of the build as a whole
lands on: the workspace file where one governs the session, else
the module file of the module the client root lies in.

**placement** (term): Where a diagnostic of a judgement lands: one
with a file and a position at that position; one with a file and no
position at the file's first line; one naming a module's directory
at that module's module file, first line; one with no location at
the build home, first line. A judgement places, beside the build's
own diagnostics, the diagnostic of every `.proto` document outside
the build on that document (REQ-lsp-outside) and the unpinned
diagnostics on the lockfile, else the build home
(REQ-lsp-unpinned). A placement in a file the build did not read
from the working tree — a dependency's, the toolchain's — is
published under the address REQ-lsp-dependency-files gives it.

**publish set** (term): The files a judgement is published for, each
with the list of every placement on it: every file a placement of
the judgement names, and every file whose last publish carried a
non-empty list, the last with an empty list where the judgement
places nothing on it — so a diagnostic once published is withdrawn
by the judgement that no longer makes it, whatever the file became.
A member whose list equals its last publish and whose document's
version has not moved is not published again.

**empty answer** (term): What a request over a position nothing is
known of returns: `null` for a definition or hover, the empty list
for references and formatting.

**position encoding** (term): The unit a column is counted in on the
wire, selected from what the client offers
(`general.positionEncodings`) in this order: `utf-8` bytes, `utf-32`
code points, `utf-16` code units — the protocol's default, and the
one selected where the client offers nothing — and stated back
(`capabilities.positionEncoding`).

## The session

**REQ-lsp-session** (behavior): On `initialize` the server MUST load
the session of the client root exactly as a check verb loads its
own — the working tree, the user configuration (read then and never
again), the trust policy, the lockfile — and resolve the build as
`pb lint` resolves it (`check-rules.md` §Verbs) with one difference:
read-only. The build list is selected and every module's and
ruleset's content read at the pins the lockfile holds; a pair the
lockfile does not pin is not resolved (REQ-lsp-unpinned), so the
server never fetches what no pin names and never writes a pin: the
lockfile is the user's record, which a verb writes
(`module-lockfile.md` REQ-lock-first-use), never an editor. A client
root that loads no session — none, a malformed workspace, module,
lint or lock file, a pinned dependency that cannot be fetched or
fails verification — leaves the server serving with no build: every
`.proto` document is then outside the build, the failure is reported
to the client as a message (`window/showMessage`, error), and the
next reload (REQ-lsp-reload) tries again.

**REQ-lsp-unpinned** (behavior): A requirement of the build — a
module pair, a ruleset import, wherever declared — that the lockfile
does not pin MUST leave the server serving with no build, as an
`initialize` that loads no session does, every judgement placing one
diagnostic per unpinned pair the resolution meets — every pair the
workspace's modules and the lint file declare, read through the
root's replacements, and the first pair beyond them the resolution
stops at, a pair a declared one requires being known only once the
declared one is pinned and read — on the lockfile where one exists,
else on the build home, at its first line, while the pair is
unpinned and required: severity error, source `pb`, code `unpinned`,
naming the pair and the verb that pins it (`pb dep download`). The
server resolves nothing on the user's behalf: a build the lockfile
does not fix is not one `pb lint` would judge the same way twice.

**REQ-lsp-reload** (behavior): The server MUST reload the session
when a file the resolution reads changes on disk — the workspace
file, a module file, the lint file, the lockfile, the trust policy,
a rule file, a `.proto` file created, changed, deleted or renamed —
as the client reports it (`workspace/didChangeWatchedFiles`, the
server registering for those names on `initialized`, whatever the
session loaded, where the client offers the registration; where it
does not, changes outside documents are seen at the next save of a
document) or as a document of such a file is saved
(`textDocument/didSave`, which the server's capabilities request). A
document of a configuration file is no overlay: the session reads
configuration from disk, so an unsaved edit to the workspace,
module, lint, lock or trust file changes nothing until saved. A
reload that fails keeps serving the last build loaded and reports
the failure as REQ-lsp-session has it; one that finds a pair
unpinned serves none, as REQ-lsp-unpinned has it.

## Documents

**REQ-lsp-overlay** (behavior): The build the server judges MUST be
the tree with every document of a build file in place of the file
at its path — the document at the client's latest version,
synchronized whole (`TextDocumentSyncKind.Full`) — and nothing of a
document reaches the tree: the server writes no file, formatting
returns edits, and a document closed leaves the tree's file to speak.
A document outside the build takes part in no compile.

**REQ-lsp-outside** (behavior): A `.proto` document outside the build
MUST receive, as a placement of every judgement while it stands, one
diagnostic, informational, at its first line, naming the first
reason of the outside-the-build term that applies; its contents
take part in no compile and no lint, the build's placements on its
file standing (a dependency's file the build read carries them);
every request over it but formatting is answered the empty answer,
formatting as REQ-lsp-formatting has it. The contents of a document
of any other kind feed no judgement, the placements on its file
standing, and every request over it is answered the empty answer.

## Diagnostics

**REQ-lsp-diagnostics** (behavior): After the session's load on
`initialized`, and after every change to what a judgement reads or
places on — a document of a build file opened, changed or closed, a
`.proto` document outside the build opened or closed, a reload — the
server MUST judge the build and publish
(`textDocument/publishDiagnostics`) for the publish set the
diagnostics the judgement places on each file, so a client holds
exactly the current judgement's diagnostics and no stale one: a
publish for an open document carries its version, one
for a closed file none, and a judgement begun for a build that a
later change superseded is dropped unpublished. The build's
diagnostics are:

1. where the build does not compile, the compile's errors — every
   error the compiler reports over the build under a reporter that
   collects them, where the verb names what failed as
   `check-rules.md` REQ-check-lint-verb has it: the server's verdict
   is the verb's, its account fuller — each at its placement: an
   import no module satisfies (`module-resolution.md`
   REQ-resolve-unsatisfied-imports) in the importing file at the
   first statement importing that path, an ambiguous provider,
   which names no file, on the build home; severity error, source
   `pb`, code `compile`; and no lint finding anywhere, as the verb
   reports none for a build that does not compile;
2. where it compiles, the lint findings `pb lint` would print
   (`check-rules.md` REQ-check-findings-output), each at its
   placement — a finding with a file and a position there, a
   `package` finding under the root's selection at its file's first
   line, a `package` or `set` finding under a module's own selection
   at that module's module file, a `set` finding under the root's on
   the build home; severity `error` as error and `warning` as
   warning; source `pb`; code the rule's name; message the rule's;
   suppressed findings absent as the verb leaves them.

Zero lint rules enabled is zero lint diagnostics, as the verb says
nothing (REQ-rules-no-defaults). Breaking rules are not evaluated:
their base is a verb's choice, not an editor's.

**REQ-lsp-positions** (wire): Every position the server sends MUST
be the protocol's — a zero-based line and a column in the position
encoding selected, lines ending at `\n`, `\r\n` or a lone `\r` as
the protocol counts them — reached through the byte offset in the
document's contents as the server holds them: a compiler position by
the offset it carries, a finding's by its one-based line and
code-point column (`check-rules.md` REQ-rules-finding-location, lines
ending at `\n` as pb counts them), the offset then counted out in
the protocol's units; a range names the token at the position where
the server knows it, else the position alone.

## Navigation

**REQ-lsp-definition** (behavior): `textDocument/definition` over a
name in an open build file MUST answer the declaration the resolved
build binds the name to — a message, enum, enum value, field, oneof,
service, method or extension named in a type, an option, an
extension, an input or output — as the location of that
declaration's name token in its file, an import's path the imported
file at its first line; the empty answer where the position names
nothing bound, and for a document not open or outside the build. A
position is over a name from its first character through the one
just past its last; a token declaring two names — a group's, which
names its field and its message — is over the field. Where the build
does not compile, the request is answered from the last build that
compiled if the document's contents are those that build read, else
the empty answer.

**REQ-lsp-hover** (behavior): `textDocument/hover` over a name in a
build file MUST answer the bound declaration's kind and full name in
a fenced `protobuf` block, followed by its leading comments as the
compiler records them, markers stripped, as markdown; the range the
name as written at the position, qualified where it is; the empty
answer where nothing is bound, under REQ-lsp-definition's rules — the
document open, the position over a name, the build that does not
compile.

**REQ-lsp-references** (behavior): `textDocument/references` over a
name or a declaration in a build file MUST answer every reference to
the bound declaration across the resolved build's files — the build
files and the dependencies' — each the location of the referring
token, the declaration itself included when the request asks for it
(`context.includeDeclaration`), in URI then position order; the
empty answer where nothing is bound, under REQ-lsp-definition's
rules — the document open, the position over a name, the build that
does not compile.

**REQ-lsp-dependency-files** (wire): A location MUST address its
file as the file's origin has it: one the build read from the
working tree as a `file://` URI of that file; a dependency's by the
client's capability: where the client offers `workspace/textDocumentContent`,
as `pb-module://<module path>@<version>/<file path>`, spelled on the
wire in the URI's canonical form — the `@` before the version
percent-encoded as `%40` where it falls in the path, a file path's
characters a URI reads as syntax percent-encoded as well — a file of
the well-known imports (`module-resolution.md`) as
`pb-module://well-known@<digest>/<file path>`, its `@` literal in the
authority — a module's locator carries the version, the well-known
set's the digest the source store names the set by below, so one
address names one toolchain's bytes as one names one pair's —
the scheme declared in
the server's capabilities and served through that request with the
bytes the build read; otherwise as a `file://` URI into the
dependency source store. The pair in a dependency file's address, and
the pair under which the source store holds it, is the pair whose
bytes the file is: a pinned replacement's where one applies
(`workspace.md` REQ-work-replace), the requirement's name kept to
the module graph, so one address names one file's bytes across every
reload and a replacement that moves moves the address with the
bytes. The source store is where the server copies a dependency's
files from the verified content the build read, whole, atomically
and read-only — a copy is the bytes its address names, made
read-only as a module cache's files are, so an editor's save in
place is refused, a copy forced past the mode replaced at the next
filling — at the first judgement that reads the build of a session,
and again at the next where the filling failed, the failure shown to
the client once until a filling succeeds and logged at every try —
`<user cache>/pb/sources/<escaped module path>@<escaped
version>.<digest>/<file path>`, the same pair, escaped as
`module-proxy.md` escapes a module path and version, the digest the
pair's archive digest spelled as the module cache spells it
(`dep-verbs.md` REQ-dep-cache-layout), so two roots' pins of one
pair are two copies, each the content its name fixes, the well-known
files under
`<user cache>/pb/sources/well-known@<digest>/` with `<digest>` the
hex SHA-256 over the toolchain's well-known imports, each file's
path then bytes in path order, every field preceded by its length as
eight big-endian bytes, so a toolchain's copy never serves
another's — a copy present is compared with the bytes the build
read and replaced where it differs, nothing else written there,
`pb clean --sources` emptying it, the read-only copies and the
copies of the layout before the digest, `<escaped module
path>@<escaped version>`, which no server reads, with it
(`dep-verbs.md` REQ-dep-clean). The content served for an address is the bytes the
last judgement read for it, else — the module gone from the build
since — those the last build that compiled read, while navigation
answers from that build (REQ-lsp-definition's rule), so an address
the server handed out is served while it stands; a request for a
path neither provides is an error naming it. Such a file is outside
the build when opened as a document (REQ-lsp-outside): it is read,
never edited.

## Formatting

**REQ-lsp-formatting** (behavior): `textDocument/formatting` over a
document that is an own file — judged by the resolution root's
module set, read whether or not a build is loaded, a workspace copy
of a file of the well-known imports among the own files — MUST
answer the edits that take the document to its canonical form
(`format.md`) — one edit replacing the whole text where the form
differs, no edit where it does not — and an error naming the parse
failure where the document does not parse, as `pb format` refuses
it; the request's options (tab size, spaces) are ignored, the form
having no options. A document that is no own file is answered the
empty answer, and so is one whose standing cannot be judged: no
resolution root, or the root unreadable as `workspace.md` reads it —
its workspace file, or a member's module file — which leaves every
file's standing unjudged.

## Lifecycle and transport

**REQ-lsp-lifecycle** (behavior): The server MUST follow the
protocol's lifecycle: `initialize` first, a request before it
answered `ServerNotInitialized` and a notification before it
dropped, `exit` excepted; `initialized` taken as the client's go;
`shutdown` ending every judgement in flight and answering, after
which every request is answered `InvalidRequest` and every
notification but `exit` dropped; `exit`, whenever it comes, ending
the process with status 0 after `shutdown` and 1 without. Requests
are answered in wire order; a cancellation (`$/cancelRequest`) has
no effect, the request it names answered as if none had come, and
no request is ever answered `RequestCancelled`. The server's
capabilities state exactly what it serves — full document sync with
open, close and save notifications, diagnostics pushed, definition,
hover, references, formatting, the `pb-module` content scheme where
the client offers the request, the position encoding selected — and
nothing it does not.

**REQ-lsp-transport** (wire): The server MUST speak over standard
input and output alone, `Content-Length` framed as the protocol has
it, and write nothing else to either; its own logging goes to
standard error. A frame whose body is no JSON-RPC message is
answered with the protocol's parse or invalid-request error and the
connection goes on; a framing error, or the connection's end on
either side, ends the server, every judgement in flight ended first,
as `shutdown` ends them.

## Invariants

**REQ-lsp-parity** (invariant): When every document equals the tree's
file at its path and the lockfile pins every requirement, the
diagnostics the server publishes MUST be `pb lint`'s verdict for
that tree: a build that compiles, the verb's findings one to one,
each at its placement and at the same line and column under
conversion; one that does not, compile errors and no lint finding,
what the verb names among them — the file and what failed in it,
the message the server's own. The server judges with the verb's
engine over the verb's build, never a build of its own.

**REQ-lsp-fresh** (invariant): A publish MUST never carry a judgement
that a change the client reported before the publish superseded: a
judgement superseded before it is published is dropped, a publish
for an open document carries the version it judged, and every file
whose last publish carried a non-empty list is in every later
judgement's publish set.

**REQ-lsp-tree-untouched** (invariant): The server MUST write nothing
under the client root's resolution root, whatever documents it
holds, edits it answers or pins it lacks; the stores under the user
cache (the dependency source store, the module cache) are the one
exception, where the user has placed the cache under the root.
