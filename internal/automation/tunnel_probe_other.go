//go:build !linux

package automation

import (
	"fmt"
	"syscall"
)

func bindTunnelProbeSocket(conn syscall.RawConn, iface, mark string) error {
	return fmt.Errorf("tunnel HTTPS probes require Linux")
}
