package runner

import "github.com/greatliontech/sandbox"

// sandboxKillExitCode is the exit code a process the sandbox ended
// exits with on windows, which has no signals: the sandbox's
// contract (its docs/specs/sandbox.md, the exit's report), the one
// code a bound's kill leaves.
const sandboxKillExitCode = 137

// killed reports the death a bound's kill is on windows: the plugin
// exited with the sandbox's kill code, which a plugin exiting 137 of
// its own accord cannot be told from — as the daemon's record cannot
// (REQ-plugin-resource-bounds).
func killed(es sandbox.ExitStatus) bool { return !es.Signaled && es.Code == sandboxKillExitCode }

// cpuSignalled: the platform has no signals; the Job's CPU kill is
// counted.
func cpuSignalled(sandbox.ExitStatus) bool { return false }

// killSpelling names the kill a report cannot attribute.
const killSpelling = "exit 137"
