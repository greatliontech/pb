package runner

import "github.com/greatliontech/sandbox"

// cpuSignalled: the Linux kernel, its soft and hard limits one, ends
// the process at the CPU-time limit rather than signalling it, and
// the sandbox counts that kill; a SIGXCPU here is another process's
// signal, never the bound's.
func cpuSignalled(sandbox.ExitStatus) bool { return false }
