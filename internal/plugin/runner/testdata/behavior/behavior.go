// Package behavior names the runner suite's fixture plugin behaviors
// once: the fixture dispatches on these spellings and the suite's
// tests select by them, so no spelling lives in two places. Which
// list a behavior belongs to is a fact the tests pin — every listed
// behavior is answered by the fixture as itself.
package behavior

import "strings"

// The behaviors, as the request parameter spells them.
const (
	Default = ""
	Net     = "net"
	Write   = "write"
	Host    = "host"
	Both    = "both"
	Env     = "env"
	Exit7   = "exit7"
	World   = "world"
	Sleep   = "sleep"
	Spin    = "spin"
	Hog     = "hog"
)

// Compared are the behaviors both runners must answer identically:
// each ends in a response that reports nothing of the runner.
var Compared = []string{Default, Net, Write, Host, Both, Env, Exit7}

// Probes report the runner's world on purpose and are compared by
// no test.
var Probes = []string{World}

// Bounding run until a bound ends them.
var Bounding = []string{Sleep, Spin, Hog}

// The world probe's paths, one source for the fixture that reports
// them and the witness that judges the report.
var (
	// RuntimeRoots are where an OCI runtime mounts its filesystems.
	RuntimeRoots = []string{"/proc", "/sys", "/dev"}
	// NameFiles are the daemon's name files bound over the image's.
	NameFiles = []string{"/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}
	// Present are the paths whose existence the probe reports.
	Present = []string{"/proc", "/sys", "/dev", "/dev/shm", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}
	// Writes are the paths the probe tries to write: a new file under
	// /dev, a runtime mask under /proc, a new file under /proc, and a
	// new file at the root.
	Writes = []string{"/dev/probe", "/proc/interrupts", "/proc/probe", "/probe"}
)

// Escape makes a path safe inside the probe's comma- and
// colon-separated report: the two separators become octal escapes,
// as mountinfo already spells space, tab, newline and backslash.
func Escape(p string) string {
	p = strings.ReplaceAll(p, `\`, `\134`)
	p = strings.ReplaceAll(p, ":", `\072`)
	return strings.ReplaceAll(p, ",", `\054`)
}

// Unescape reverses Escape.
func Unescape(p string) string {
	p = strings.ReplaceAll(p, `\054`, ",")
	p = strings.ReplaceAll(p, `\072`, ":")
	return strings.ReplaceAll(p, `\134`, `\`)
}
