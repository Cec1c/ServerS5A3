//go:build windows

package dnfbridge

import (
	"encoding/binary"
	"math/bits"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func localPlayerHTTPProcess(r *http.Request) (uint32, time.Time, bool) {
	peerHost, peerPort, err := net.SplitHostPort(r.RemoteAddr)
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if err != nil || !ok || net.ParseIP(peerHost) == nil || !net.ParseIP(peerHost).IsLoopback() {
		return 0, time.Time{}, false
	}
	localHost, localPort, err := net.SplitHostPort(local.String())
	if err != nil || net.ParseIP(localHost) == nil || !net.ParseIP(localHost).IsLoopback() || r.Host != local.String() {
		return 0, time.Time{}, false
	}
	pp, pe := strconv.ParseUint(peerPort, 10, 16)
	lp, le := strconv.ParseUint(localPort, 10, 16)
	if pe != nil || le != nil || pp == 0 || lp == 0 {
		return 0, time.Time{}, false
	}
	var size uint32
	result, _, _ := procGetExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(syscall.AF_INET), tcpTableOwnerPIDAll, 0)
	if result != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 32<<20 {
		return 0, time.Time{}, false
	}
	table := make([]byte, size)
	result, _, _ = procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&table[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(syscall.AF_INET), tcpTableOwnerPIDAll, 0)
	if result != 0 || int(size) > len(table) {
		return 0, time.Time{}, false
	}
	pid, ok := playerLoopbackOwnerFromTable(table[:size], uint16(pp), uint16(lp), net.ParseIP(peerHost).To4(), net.ParseIP(localHost).To4())
	if !ok {
		return 0, time.Time{}, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, time.Time{}, false
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) != nil {
		return 0, time.Time{}, false
	}
	return pid, time.Unix(0, created.Nanoseconds()), true
}

func playerLoopbackOwnerFromTable(table []byte, peerPort, localPort uint16, peerIP, localIP net.IP) (uint32, bool) {
	if len(table) < 4 || peerIP.To4() == nil || localIP.To4() == nil {
		return 0, false
	}
	count := uint64(binary.LittleEndian.Uint32(table[:4]))
	if count > uint64((len(table)-4)/24) {
		return 0, false
	}
	var matched uint32
	for i := uint64(0); i < count; i++ {
		row := table[4+i*24 : 4+(i+1)*24]
		if binary.LittleEndian.Uint32(row[:4]) != 5 ||
			!net.IP(row[4:8]).Equal(peerIP) || !net.IP(row[12:16]).Equal(localIP) ||
			bits.ReverseBytes16(uint16(binary.LittleEndian.Uint32(row[8:12]))) != peerPort ||
			bits.ReverseBytes16(uint16(binary.LittleEndian.Uint32(row[16:20]))) != localPort {
			continue
		}
		pid := binary.LittleEndian.Uint32(row[20:24])
		if pid == 0 || matched != 0 {
			return 0, false
		}
		matched = pid
	}
	return matched, matched != 0
}
