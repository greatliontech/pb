# A local plugin's pin names bytes its run may not execute

The `local` scheme hashes the resolved binary at acquisition and pins
the hash (plugin-execution.md REQ-plugin-local-pin: "identity is the
bytes, not the location"), then the run executes the binary by its path
later, after the other plugins of the generation have run. A binary
rebuilt in that window — a developer's `go build` of their own plugin
while `pb generate` runs — executes bytes the pin never hashed, under a
pin that claims them: a content name (the hash) attached to a place (the
path) at the moment of use.

The shape: the acquirer copies the resolved binary into a directory the
generation run owns and removes with the run, hashes the copy, pins that
hash and hands the run the copy's path as the process's argv[0] — the
bytes executed are the bytes hashed, by construction, on every platform
— with the working directory the resolution root as now; a plugin that
finds resources beside its own executable (`os.Executable`) then finds
the run's directory, which the clause states, the local scheme's
arguments and working directory being the configuration a plugin reads
its surroundings from. The run's directory is a new lifecycle (created
at the generation's start, removed at its end, a crash's residue removed
by the next run), stated in plugin-execution.md's local scheme beside
REQ-plugin-local-pin and witnessed by a binary replaced between the
acquisition and the run executing the acquired bytes.

Lands: 34.
