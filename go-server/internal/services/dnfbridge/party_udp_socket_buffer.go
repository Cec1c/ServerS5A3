package dnfbridge

import (
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	// partyUDPSocketBufferBytesEnv 配置内嵌组队 UDP 中继与原生探针监听器的套接字读写缓冲区请求字节数。
	// 默认请求 2 MiB（Linux 内核实际记账上限约 4 MiB，并受宿主机 net.core.rmem_max/wmem_max 夹紧）；
	// 置 0 保持操作系统默认，置 65536..16777216 按指定字节数请求并读回实际 SO_RCVBUF/SO_SNDBUF。
	partyUDPSocketBufferBytesEnv     = "DNFBRIDGE_PARTY_UDP_SOCKET_BUFFER_BYTES"
	defaultPartyUDPSocketBufferBytes = 2 * 1024 * 1024
	minPartyUDPSocketBufferBytes     = 64 * 1024
	maxPartyUDPSocketBufferBytes     = 16 * 1024 * 1024
)

type partyUDPSocketBufferState struct {
	RequestedBytes int
	EffectiveRead  int
	EffectiveWrite int
	SetReadErr     error
	SetWriteErr    error
	ReadbackErr    error
}

func (state partyUDPSocketBufferState) clamped() bool {
	expected := socketBufferAccountingFactor * state.RequestedBytes
	return state.EffectiveRead < expected || state.EffectiveWrite < expected
}

func readPartyUDPSocketBufferBytes() (bytes int, raw string, invalid bool) {
	raw = strings.TrimSpace(os.Getenv(partyUDPSocketBufferBytesEnv))
	if raw == "" {
		return defaultPartyUDPSocketBufferBytes, "", false
	}
	lower := strings.ToLower(raw)
	if lower == "0" || lower == "default" || lower == "system" || lower == "off" {
		return 0, raw, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minPartyUDPSocketBufferBytes || parsed > maxPartyUDPSocketBufferBytes {
		return defaultPartyUDPSocketBufferBytes, raw, true
	}
	return parsed, raw, false
}

func configurePartyUDPSocketBuffer(conn *net.UDPConn, requestedBytes int) partyUDPSocketBufferState {
	state := partyUDPSocketBufferState{RequestedBytes: requestedBytes}
	if conn == nil {
		return state
	}
	if requestedBytes > 0 {
		state.SetReadErr = conn.SetReadBuffer(requestedBytes)
		state.SetWriteErr = conn.SetWriteBuffer(requestedBytes)
	}
	state.EffectiveRead, state.EffectiveWrite, state.ReadbackErr = readUDPSocketBufferSizes(conn)
	return state
}

func (r *currentPartyUDPRelay) applySocketBuffer(conn *net.UDPConn, port int, role string) partyUDPSocketBufferState {
	if r == nil || conn == nil {
		return partyUDPSocketBufferState{}
	}
	state := configurePartyUDPSocketBuffer(conn, r.socketBufferBytes)
	if state.EffectiveRead > 0 {
		r.lastEffectiveRcvbuf.Store(int64(state.EffectiveRead))
	}
	if state.EffectiveWrite > 0 {
		r.lastEffectiveSndbuf.Store(int64(state.EffectiveWrite))
	}
	if r.socketBufferBytes > 0 {
		if state.SetReadErr != nil || state.SetWriteErr != nil || state.ReadbackErr != nil {
			if r.loggedSocketBufferIssue.CompareAndSwap(false, true) && r.logWarn != nil {
				r.logWarn("party UDP relay socket buffer configuration failed",
					"role", role,
					"port", port,
					"requested_bytes", r.socketBufferBytes,
					"read_error", state.SetReadErr,
					"write_error", state.SetWriteErr,
					"readback_error", state.ReadbackErr)
			}
		} else if state.clamped() {
			// Linux clamps SO_RCVBUF to 2*rmem_max without failing setsockopt;
			// surface a single bounded warning when the host cap still truncates it.
			if r.loggedSocketBufferClamp.CompareAndSwap(false, true) && r.logWarn != nil {
				r.logWarn("party UDP relay socket buffer clamped by kernel limit",
					"role", role,
					"port", port,
					"requested_bytes", r.socketBufferBytes,
					"effective_rcvbuf_bytes", state.EffectiveRead,
					"effective_sndbuf_bytes", state.EffectiveWrite)
			}
		}
	}
	return state
}
