# pb — export

Export materializes a resolution root's protobuf sources onto a
filesystem for tooling outside pb — a protoc invocation, a buf
workspace, an editor's include path, a vendored tree — as one include
tree the lockfile's build determines. Inside pb no compiler is handed
a dependency's files laid out on disk: every consumer reads the
verified archive. Export is the one sanctioned way out, and it
inherits every rule the build already obeys: the build list is the
lockfile's, imports resolve exactly as generation resolves them, and
materialization writes what `module-archive.md` permits and nothing
else.

**export** (term): The verb `pb export <dir>`, run against the
resolution root governing the working directory, located and held to
membership as `dep-verbs.md` locates a dep verb's root.

**export tree** (term): The directory an export writes: every exported
file at its import path under the output directory, one include root
for every module of the build.

**import closure** (term): The workspace modules' protobuf files and
every file they import, transitively, across the build's modules; a
well-known import (`module-resolution.md`) is the toolchain's and never
among them.

**REQ-export-build** (behavior): An export MUST compile the build
exactly as generation does (`generation.md` REQ-gen-compile) before
writing anything: the build list resolved and its first-use pins
persisted (`module-lockfile.md` REQ-lock-first-use), every workspace
module's files with imports resolved first against the well-known
imports and then against the build list's modules, an unsatisfied
import failing per `module-resolution.md`
REQ-resolve-unsatisfied-imports, and an import path more than one
module provides failing naming the path and every provider — so the
workspace modules' files and everything they reach are the build a
consumer would compile, or nothing is written.

**REQ-export-selection** (behavior): An export MUST write the import
closure by default and, under `--all`, every protobuf file of every
workspace module and every build-list module, a synthesized module
(`module-resolution.md` REQ-resolve-synthesis) among them, a file no
workspace file reaches written as its archive holds it and compiled
by no one; in either mode a well-known import path is never written —
the toolchain's copy answers for it in every compile and a consumer's
compiler ships its own — and a module's other files, its module file
and rule files among them, are no part of an export tree.

**REQ-export-exclusion** (behavior): `--exclude <module path>`,
repeatable, MUST remove the named module's files from what is written
and nothing else: the closure is computed over the whole build, so a
file an excluded module's files import is written whenever its own
module is not excluded, and an exclusion the closure never reaches
removes nothing; a path naming no build-list module, naming a
workspace module, naming a replaced path (`workspace.md`
REQ-work-replace, REQ-work-replace-dir: the build read its
replacement, which no pin under that path names, so nothing the
consumer could supply and hold to a pin stands for it), or given
twice fails the export before anything is written; and every excluded
module is reported with its selected version, the consumer supplying
that module and holding it to the pin.

**REQ-export-layout** (wire): An export tree MUST hold each exported
file at its include-root-relative path under the output directory, as
a regular file, and nothing else — no marker, no manifest, no file of
pb's own — the exported paths of every module taken together being
one valid file set under `module-archive.md` REQ-archive-path-rules
and REQ-archive-case-collision: two modules providing one path never
reach a tree, the build having been refused (REQ-export-build), and
two exported paths equal under case folding, or one folding equal to
a directory prefix another implies, fail the export before anything
is written, naming the paths and their modules — a tree holds every
file on a case-insensitive filesystem as on a case-sensitive one —
and a workspace file violating the path rules fails the export as it
would fail its module's archive.

**REQ-export-materialization** (invariant): An export MUST write
regular, non-executable files only (`module-archive.md`
REQ-archive-no-exec-materialization): a build-list module's from the
verified bytes of its archive the build loaded — a link or a
submodule entry the archive carries is never written and never
followed, whatever its name, and a file's executable mode is not
carried onto disk — and a workspace module's from its working copy,
the bytes generation compiles, written as regular files whatever the
working copy's modes.

**REQ-export-output** (behavior): The output directory, a required
argument, MUST be absent or an empty directory when the export begins,
its parent directory existing, and lie, with symbolic links resolved
and a path through a dangling link refused, outside every workspace
module's directory — judged by the directory's identity, not its
spelling: files under one are that module's on the next load, so the
tree of a single-module workspace, whose module directory is the
workspace root (`workspace.md` REQ-work-default), lands outside the
project — and the tree lands whole or not at all: written in full into
a temporary sibling of its destination and moved into place by one
rename, an empty destination removed just before it, a failure
removing the sibling and leaving the destination as the export found
it, an interruption leaving at most the sibling, which no later
export reads, and, when interrupted between the removal and the move,
the empty destination absent.

**REQ-export-report** (behavior): An export MUST report on standard
output one line per module of the build in build order — workspace
modules in the order the workspace file uses them, then build-list
modules sorted by module path — naming the module (a build-list
module with its selected version, a replaced one as `workspace.md`
REQ-work-replace and REQ-work-replace-dir render it) and its count of
files in the tree or that it was excluded, then one line with the
total count and the output directory as given.

**REQ-export-determinism** (invariant): An export tree and its report
MUST be a pure function of the workspace modules' sources, the pinned
build and the flags — byte-identical across runs, independent of cache
state and of traversal order (`module-resolution.md`
REQ-resolve-determinism).
