//go:build linux

package automation

import (
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func bindTunnelProbeSocket(conn syscall.RawConn, iface, mark string) error {
	var socketErr error
	err := conn.Control(func(fd uintptr) {
		if socketErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface); socketErr != nil {
			return
		}
		if mark != "" {
			var value uint64
			value, socketErr = strconv.ParseUint(mark, 0, 32)
			if socketErr == nil {
				socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(value))
			}
		}
	})
	if err != nil {
		return err
	}
	return socketErr
}
