//go:build linux || darwin

package runner

import (
	"syscall"

	"github.com/greatliontech/sandbox"
)

// killed reports the death a bound's kill is on the unix rows: the
// plugin ended by SIGKILL, not by an exit of its own.
func killed(es sandbox.ExitStatus) bool { return es.Signaled && es.Signal == syscall.SIGKILL }

// killSpelling names the kill a report cannot attribute.
const killSpelling = "SIGKILL"
