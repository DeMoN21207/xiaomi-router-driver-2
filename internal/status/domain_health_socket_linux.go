//go:build linux

package status

import (
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func applyDomainHealthSocketOptions(c syscall.RawConn, interfaceName string, fwMark string) error {
	interfaceName = strings.TrimSpace(interfaceName)
	fwMark = strings.TrimSpace(fwMark)

	var controlErr error
	if err := c.Control(func(fd uintptr) {
		if interfaceName != "" {
			if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, interfaceName); err != nil {
				controlErr = err
				return
			}
		}

		if fwMark == "" {
			return
		}

		value, err := strconv.ParseInt(fwMark, 0, 32)
		if err != nil {
			controlErr = err
			return
		}
		if value <= 0 {
			return
		}
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(value)); err != nil {
			controlErr = err
		}
	}); err != nil {
		return err
	}

	return controlErr
}
