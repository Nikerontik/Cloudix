//go:build windows

package discovery

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// reuseControl sets SO_REUSEADDR before bind. Windows has no SO_REUSEPORT;
// SO_REUSEADDR alone lets multiple sockets bind the same UDP port, which is
// what the unicast discovery listener needs to share :47990 with the
// multicast listener.
func reuseControl(_, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
