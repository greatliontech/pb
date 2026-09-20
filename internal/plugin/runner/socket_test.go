package runner

import "net"

// unixSocketAt creates a unix socket file at path.
func unixSocketAt(path string) error {
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	return l.Close()
}
