//go:build !windows

package dnfbridge

import (
	"net"
	"syscall"
)

const socketBufferAccountingFactor = 2

func readUDPSocketBufferSizes(conn *net.UDPConn) (rcvbuf, sndbuf int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var sysErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		rcvbuf, sysErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		if sysErr != nil {
			return
		}
		sndbuf, sysErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if ctrlErr != nil {
		return 0, 0, ctrlErr
	}
	return rcvbuf, sndbuf, sysErr
}
