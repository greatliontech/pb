# pb — user configuration

pb's machine-scoped settings — the runner, the module proxy and its
exclusions, the module cache, the trusted root, the plugin byte
path — are the machine's, never a project's: none is committed, and
each is read the same way. This document defines where they live
and how the layers that can state one rank; the settings themselves
are defined where their subjects are (`plugin-execution.md`,
`module-proxy.md`, `dep-verbs.md`, `provenance.md`).

**setting** (term): One machine-scoped value pb reads at startup,
named by a key in the user configuration file and by an environment
variable, and for some settings by a flag of the verb it governs:
`runner` / `PBRUNNER` / `--runner`, `proxy` / `PBPROXY`, `noproxy` /
`PBNOPROXY`, `cache` / `PBCACHE`, `trustedroot` / `PBTRUSTEDROOT`,
`plugin-pull` / `PBPLUGINPULL`. The key and the variable name one
setting; their values have one grammar, the subject document's.
`cache` and `trustedroot` are path-valued: their values name a
filesystem path.

**user configuration file** (term): The file `pb/config.yaml` under
the platform's user configuration directory — on Unix
`$XDG_CONFIG_HOME`, else `~/.config`; on macOS the user's Application
Support directory; on Windows `%AppData%`; on Plan 9 `$home/lib` —
read at startup when present. On a host with no home to derive that
directory from, the file is absent: its location is the file layer's
alone to need, and its absence never defeats a value another layer
states. A location the user stated and the platform cannot use (a
relative `$XDG_CONFIG_HOME`) is an error, never a silently ignored
file. pb never writes it.

**REQ-uc-format** (wire): The user configuration file MUST be one
YAML mapping from setting keys to non-empty string scalars, under the
contract-file admissibility rules (no anchors, aliases, merge keys or
tags; string keys) — a key that names no setting, a value that is not
a non-empty string scalar, or any other document shape is an error
naming the file and the offending key, never a silently ignored
entry. An empty value is refused rather than read as an absent key:
the file is hand-edited, and an empty scalar there is a slip, not the
shell's idiom for unsetting a variable that an empty environment
value is.

**REQ-uc-precedence** (behavior): Every setting MUST resolve by
layer: the verb's flag where given, over the environment variable
where set to a non-empty value, over the file's key where present,
over the setting's own default; a value is taken from one layer whole
and the layers below it are not consulted, and the layer a value came
from is named in any refusal of it.

**REQ-uc-paths** (behavior): A path-valued setting the user
configuration file states as a relative path MUST be resolved against
the file's own directory, never the working directory — the file is
machine-scoped and its meaning must not change with where pb is
invoked. An absolute path, and any path from the environment or a
flag, is taken as given.
