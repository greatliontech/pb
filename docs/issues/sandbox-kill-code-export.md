# The sandbox's kill exit code is restated in pb

On windows a process the sandbox ended exits with 137 (the sandbox's
`docs/specs/sandbox.md`, the exit's report), the platform having no
signals; pb's runner reads that death as a bound's kill
(`internal/plugin/runner/kill_windows.go`, `sandboxKillExitCode`).
The sandbox keeps the code unexported (`killExitCode`), so pb restates
a fact of the sandbox's contract rather than reading it.

Resolution: the sandbox exports the code and pb reads it, the
restatement deleted.

Lands: a sandbox release exporting the kill exit code is pb's
dependency.
