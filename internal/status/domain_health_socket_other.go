//go:build !linux

package status

import "syscall"

func applyDomainHealthSocketOptions(_ syscall.RawConn, _ string, _ string) error {
	return nil
}
