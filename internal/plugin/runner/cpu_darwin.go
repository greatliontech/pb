package runner

import (
	"syscall"

	"github.com/greatliontech/sandbox"
)

// cpuSignalled reports a death by SIGXCPU: the kernel's own label for
// the CPU-time bound at its limit, which darwin delivers as a signal
// a payload may handle, ahead of the watchdog's sample; the rlimit's
// doing, whatever accounts for memory and processes.
func cpuSignalled(es sandbox.ExitStatus) bool { return es.Signaled && es.Signal == syscall.SIGXCPU }
