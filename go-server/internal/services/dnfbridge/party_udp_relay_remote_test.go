package dnfbridge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件钉住 2026-09-25 "厂商境外 UDP 中继控制通道客户端"
// （party_udp_relay_remote.go）的语义：
//
//	① HELLO 的 HMAC 字节与厂商 PartyRelayProtocol.ComputeHelloMac 逐字节一致；
//	② SYNC/SYNC_OK/CLOSE 的帧布局（**全部大端**）与厂商一致；
//	③ SYNC_OK 解析成 (owner,peer)→port 矩阵，并喂进 Endpoint；
//	④ SYNC 超时 ⇒ 返回"不可用"（调用方整份回退直连名单），且不重复阻塞；
//	⑤ 10s 续租 / 重连后立即补齐 / CLOSE 交还租约；
//	⑥ 房间号命名空间（ROOM_BASE + partyID，partyID=0 不注册）；
//	⑦ **密钥绝不进日志/事件**；未配或非法密钥 ⇒ 回退 embedded 并记具名事件。
//
// 假中继（partyRemoteTestServer）是**独立实现**：它自己按 wire 字节解析帧，
// 不调用生产侧的 decode 函数——否则布局错误会在两边同时"自洽"而漏检。

const partyRemoteTestHelloMACContext = "A21RELAY-HELLO/v1"

// partyRemoteTestSecret 是单测固定密钥（64 hex = 32 字节）。
const partyRemoteTestSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// ---------------------------------------------------------------------------
// 假中继
// ---------------------------------------------------------------------------

type partyRemoteTestSyncFrame struct {
	conn    int
	scope   byte
	roomID  uint32
	mode    byte
	members []uint16
	raw     []byte
}

type partyRemoteTestServer struct {
	listener net.Listener
	secret   []byte

	mu            sync.Mutex
	helloFrames   [][]byte
	closeFrames   [][]byte
	syncFrames    []partyRemoteTestSyncFrame
	connections   int
	activeByConn  map[int]net.Conn
	helloRejected bool
	noReply       bool
	closed        bool
}

func newPartyRemoteTestServer(t *testing.T, secret []byte) *partyRemoteTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake relay: %v", err)
	}
	server := &partyRemoteTestServer{
		listener:     listener,
		secret:       secret,
		activeByConn: make(map[int]net.Conn),
	}
	go server.acceptLoop()
	t.Cleanup(func() { server.Close() })
	return server
}

func (s *partyRemoteTestServer) addr() string { return s.listener.Addr().String() }

func (s *partyRemoteTestServer) hostPort() (string, int) {
	host, portText, err := net.SplitHostPort(s.addr())
	if err != nil {
		return "", 0
	}
	port := 0
	for index := 0; index < len(portText); index++ {
		port = port*10 + int(portText[index]-'0')
	}
	return host, port
}

func (s *partyRemoteTestServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.connections++
		index := s.connections
		s.activeByConn[index] = conn
		s.mu.Unlock()
		go s.serveConn(conn, index)
	}
}

func (s *partyRemoteTestServer) serveConn(conn net.Conn, index int) {
	defer func() {
		s.mu.Lock()
		delete(s.activeByConn, index)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	opcode, payload, err := readPartyRelayFrame(conn)
	if err != nil || opcode != 0x01 {
		return
	}
	s.mu.Lock()
	s.helloFrames = append(s.helloFrames, append([]byte(nil), payload...))
	reject := s.helloRejected
	s.mu.Unlock()
	if reject {
		return
	}
	// 假中继在**测试侧独立**重算 HMAC，逐字节比对（不调用生产侧 partyRelayHelloMAC）。
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(partyRemoteTestHelloMACContext))
	expected := mac.Sum(nil)
	if len(payload) != 1+len(expected) || payload[0] != 2 || !hmac.Equal(payload[1:], expected) {
		// 认证不通过 ⇒ 与厂商一致：直接断开（客户端会重连并重试）。
		return
	}
	if err := writePartyRelayFrame(conn, 0x81, nil, time.Second); err != nil {
		return
	}

	for {
		opcode, payload, err := readPartyRelayFrame(conn)
		if err != nil {
			return
		}
		switch opcode {
		case 0x02:
			frame := partyRemoteTestSyncFrame{conn: index, raw: append([]byte(nil), payload...)}
			if len(payload) < 8 {
				return
			}
			frame.scope = payload[0]
			frame.roomID = binary.BigEndian.Uint32(payload[1:5])
			frame.mode = payload[5]
			count := int(binary.BigEndian.Uint16(payload[6:8]))
			if len(payload) != 8+count*2 || count > 8 {
				return
			}
			for i := 0; i < count; i++ {
				frame.members = append(frame.members,
					binary.BigEndian.Uint16(payload[8+i*2:10+i*2]))
			}
			s.mu.Lock()
			s.syncFrames = append(s.syncFrames, frame)
			s.mu.Unlock()
			if s.replyDisabled() {
				continue
			}
			if err := writePartyRelayFrame(conn, 0x82, partyRemoteTestSyncOK(frame.roomID, frame.members), time.Second); err != nil {
				return
			}
		case 0x03:
			s.mu.Lock()
			s.closeFrames = append(s.closeFrames, append([]byte(nil), payload...))
			s.mu.Unlock()
			if len(payload) >= 5 {
				roomID := binary.BigEndian.Uint32(payload[1:5])
				_ = writePartyRelayFrame(conn, 0x84, partyRemoteTestRoomIDPayload(roomID), time.Second)
			}
		}
	}
}

// partyRemoteTestSyncOK 造 SYNC_OK：u32 BE roomId + u16 BE count + count×(owner,peer,port)。
// 端口 = 30000 + owner_index*8 + peer_index（镜像厂商"每对方向独占一个端口"）。
func partyRemoteTestSyncOK(roomID uint32, members []uint16) []byte {
	payload := make([]byte, 6)
	binary.BigEndian.PutUint32(payload[0:4], roomID)
	pairs := make([][3]uint16, 0, len(members)*(len(members)-1))
	for ownerIndex, owner := range members {
		for peerIndex, peer := range members {
			if owner == peer {
				continue
			}
			pairs = append(pairs, [3]uint16{owner, peer, uint16(30000 + ownerIndex*8 + peerIndex)})
		}
	}
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(pairs)))
	for _, pair := range pairs {
		entry := make([]byte, 6)
		binary.BigEndian.PutUint16(entry[0:2], pair[0])
		binary.BigEndian.PutUint16(entry[2:4], pair[1])
		binary.BigEndian.PutUint16(entry[4:6], pair[2])
		payload = append(payload, entry...)
	}
	return payload
}

func partyRemoteTestRoomIDPayload(roomID uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, roomID)
	return payload
}

func partyRemoteTestLegPort(members []uint16, owner, peer uint16) (uint16, bool) {
	ownerIndex, peerIndex := -1, -1
	for index, member := range members {
		if member == owner {
			ownerIndex = index
		}
		if member == peer {
			peerIndex = index
		}
	}
	if ownerIndex < 0 || peerIndex < 0 || owner == peer {
		return 0, false
	}
	return uint16(30000 + ownerIndex*8 + peerIndex), true
}

func (s *partyRemoteTestServer) replyDisabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noReply
}

// noReply / helloRejected / 主动断链都是"单变量"开关，供各用例单独打开。
func (s *partyRemoteTestServer) setNoReply(value bool) {
	s.mu.Lock()
	s.noReply = value
	s.mu.Unlock()
}

func (s *partyRemoteTestServer) setHelloRejected(value bool) {
	s.mu.Lock()
	s.helloRejected = value
	s.mu.Unlock()
}

func (s *partyRemoteTestServer) connectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

func (s *partyRemoteTestServer) helloPayloads() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]byte, 0, len(s.helloFrames))
	for _, payload := range s.helloFrames {
		result = append(result, append([]byte(nil), payload...))
	}
	return result
}

func (s *partyRemoteTestServer) syncSnapshot() []partyRemoteTestSyncFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]partyRemoteTestSyncFrame, 0, len(s.syncFrames))
	for _, frame := range s.syncFrames {
		copied := frame
		copied.members = append([]uint16(nil), frame.members...)
		result = append(result, copied)
	}
	return result
}

func (s *partyRemoteTestServer) closeSnapshot() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]byte, 0, len(s.closeFrames))
	for _, payload := range s.closeFrames {
		result = append(result, append([]byte(nil), payload...))
	}
	return result
}

func (s *partyRemoteTestServer) dropConnections() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.activeByConn))
	for _, conn := range s.activeByConn {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (s *partyRemoteTestServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.activeByConn))
	for _, conn := range s.activeByConn {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	_ = s.listener.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (s *partyRemoteTestServer) noReplyEnabled() bool { return s.replyDisabled() }

// ---------------------------------------------------------------------------
// 事件收集器 + 客户端夹具
// ---------------------------------------------------------------------------

type partyRemoteTestEvent struct {
	name   string
	fields []any
}

type partyRemoteTestEvents struct {
	mu     sync.Mutex
	events []partyRemoteTestEvent
}

func (log *partyRemoteTestEvents) record(name string, args ...any) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, partyRemoteTestEvent{name: name, fields: append([]any(nil), args...)})
}

func (log *partyRemoteTestEvents) snapshot() []partyRemoteTestEvent {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]partyRemoteTestEvent(nil), log.events...)
}

func (log *partyRemoteTestEvents) names() []string {
	var out []string
	for _, event := range log.snapshot() {
		out = append(out, event.name)
	}
	return out
}

func (log *partyRemoteTestEvents) contains(name string) bool {
	for _, candidate := range log.names() {
		if candidate == name {
			return true
		}
	}
	return false
}

func (log *partyRemoteTestEvents) count(name string) int {
	total := 0
	for _, candidate := range log.names() {
		if candidate == name {
			total++
		}
	}
	return total
}

// text 把全部事件拼成一行文本（用于"密钥绝不出现"的断言）。
func (log *partyRemoteTestEvents) text() string {
	var builder strings.Builder
	for _, event := range log.snapshot() {
		builder.WriteString(event.name)
		for _, field := range event.fields {
			builder.WriteString(" ")
			builder.WriteString(fmt.Sprintf("%v", field))
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// firstField 取某条事件里指定字段的值（字符串形式），没有则返回 ""。
func (log *partyRemoteTestEvents) firstField(name, key string) string {
	for _, event := range log.snapshot() {
		if event.name != name {
			continue
		}
		for index := 0; index+1 < len(event.fields); index += 2 {
			if fmt.Sprintf("%v", event.fields[index]) == key {
				return fmt.Sprintf("%v", event.fields[index+1])
			}
		}
	}
	return ""
}

// newPartyRemoteTestClient 起一个指向假中继的客户端（节拍压到毫秒级）。
// mutate 用来按用例改单变量（roomBase / 端口 / 超时）。
func newPartyRemoteTestClient(t *testing.T, server *partyRemoteTestServer, mutate func(*partyUDPRelayRemoteSettings)) (*currentPartyUDPRelayRemote, *partyRemoteTestEvents) {
	t.Helper()
	host, port := server.hostPort()
	events := &partyRemoteTestEvents{}
	settings := partyUDPRelayRemoteSettings{
		endpoint:       server.addr(),
		host:           host,
		port:           port,
		publicIP:       [4]byte{192, 0, 2, 12},
		secret:         server.secret,
		roomBase:       defaultPartyUDPRelayRemoteRoomBase,
		onEvent:        events.record,
		connectTimeout: 2 * time.Second,
		syncTimeout:    2 * time.Second,
		writeTimeout:   time.Second,
		refreshEvery:   60 * time.Millisecond,
		freshWindow:    time.Millisecond,
		minBackoff:     10 * time.Millisecond,
		maxBackoff:     40 * time.Millisecond,
		stableAfter:    10 * time.Second,
	}
	if mutate != nil {
		mutate(&settings)
	}
	client, err := newCurrentPartyUDPRelayRemote(settings)
	if err != nil {
		t.Fatalf("new remote relay client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, events
}

func partyRemoteTestWaitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func partyRemoteTestBindings(ids ...uint16) []currentPartyUDPRelayBinding {
	bindings := make([]currentPartyUDPRelayBinding, 0, len(ids))
	for _, id := range ids {
		bindings = append(bindings, currentPartyUDPRelayBinding{
			characterID: id,
			generation:  1,
			address:     [4]byte{10, 0, 0, byte(id % 250)},
		})
	}
	return bindings
}

// ---------------------------------------------------------------------------
// ① HELLO
// ---------------------------------------------------------------------------

func TestPartyRemoteHelloFrameMatchesVendorProtocol(t *testing.T) {
	secret, err := hex.DecodeString(partyRemoteTestSecret)
	if err != nil {
		t.Fatalf("decode fixture secret: %v", err)
	}
	server := newPartyRemoteTestServer(t, secret)
	client, _ := newPartyRemoteTestClient(t, server, nil)

	partyRemoteTestWaitFor(t, 2*time.Second, "HELLO frame", func() bool {
		return len(server.helloPayloads()) > 0
	})
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)

	payload := server.helloPayloads()[0]
	if len(payload) != 33 {
		t.Fatalf("HELLO payload len = %d, want 33 (u8 version + 32B HMAC)", len(payload))
	}
	if payload[0] != 2 {
		t.Fatalf("HELLO version byte = %d, want 2", payload[0])
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(partyRemoteTestHelloMACContext))
	if !bytes.Equal(payload[1:], mac.Sum(nil)) {
		t.Fatalf("HELLO HMAC = % X, want % X", payload[1:], mac.Sum(nil))
	}
	// 帧头必须是 u8 opcode + u16 BE length。
	frame := encodePartyRelayFrame(0x01, payload)
	if frame[0] != 0x01 || frame[1] != 0x00 || frame[2] != 0x21 {
		t.Fatalf("HELLO frame header = % X, want 01 00 21", frame[:3])
	}
}

// ---------------------------------------------------------------------------
// ② SYNC 帧布局 + ③ 矩阵喂进 Endpoint
// ---------------------------------------------------------------------------

func TestPartyRemoteSyncFrameLayoutAndMatrixFeedEndpoint(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, _ := newPartyRemoteTestClient(t, server, nil)
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)

	// 成员顺序故意乱着给：线上必须按厂商规则归一化成升序，且 partyID 映射到 ROOM_BASE+partyID。
	if !client.Sync(7, partyRemoteTestBindings(300, 5)) {
		t.Fatal("Sync refused the room")
	}
	frames := server.syncSnapshot()
	if len(frames) == 0 {
		t.Fatal("fake relay received no SYNC frame")
	}
	frame := frames[0]
	if frame.scope != 0 {
		t.Fatalf("SYNC scope = %d, want 0 (party)", frame.scope)
	}
	if want := uint32(defaultPartyUDPRelayRemoteRoomBase + 7); frame.roomID != want {
		t.Fatalf("SYNC room_id = %d, want %d (ROOM_BASE + partyID)", frame.roomID, want)
	}
	if frame.mode != 0 {
		t.Fatalf("SYNC mode = %d, want 0 (member keys)", frame.mode)
	}
	if len(frame.members) != 2 || frame.members[0] != 5 || frame.members[1] != 300 {
		t.Fatalf("SYNC members = %v, want [5 300] (升序归一化)", frame.members)
	}
	// 逐字节核对大端布局：scope | u32 roomId | mode | u16 count | count×u16 key。
	want := []byte{0x00, 0x00, 0x01, 0x86, 0xa7, 0x00, 0x00, 0x02, 0x00, 0x05, 0x01, 0x2c}
	if !bytes.Equal(frame.raw, want) {
		t.Fatalf("SYNC payload = % X, want % X", frame.raw, want)
	}
	if header := encodePartyRelayFrame(0x02, frame.raw); header[0] != 0x02 || header[1] != 0x00 || header[2] != byte(len(frame.raw)) {
		t.Fatalf("SYNC frame header = % X", header[:3])
	}
	if cached := client.cachedRoomCount(); cached != 1 {
		t.Fatalf("cached rooms = %d, want 1", cached)
	}

	address, portAB, ok := client.Endpoint(7, 300, 5)
	if !ok {
		t.Fatal("Endpoint(300->5) missing after SYNC_OK")
	}
	if address != [4]byte{192, 0, 2, 12} {
		t.Fatalf("relay ip = % X, want 192.0.2.12", address)
	}
	wantAB, _ := partyRemoteTestLegPort([]uint16{5, 300}, 300, 5)
	if portAB != wantAB {
		t.Fatalf("Endpoint(300->5) port = %d, want %d", portAB, wantAB)
	}
	_, portBA, ok := client.Endpoint(7, 5, 300)
	if !ok {
		t.Fatal("Endpoint(5->300) missing")
	}
	wantBA, _ := partyRemoteTestLegPort([]uint16{5, 300}, 5, 300)
	if portBA != wantBA || portBA == portAB {
		t.Fatalf("Endpoint(5->300) port = %d (want %d, distinct from %d)", portBA, wantBA, portAB)
	}
	if _, _, ok := client.Endpoint(7, 300, 300); ok {
		t.Fatal("Endpoint must refuse owner==peer")
	}
	if _, _, ok := client.Endpoint(8, 300, 5); ok {
		t.Fatal("Endpoint must refuse an unregistered room")
	}
}

// ---------------------------------------------------------------------------
// ⑥ 房间号命名空间：ROOM_BASE 生效、partyID=0 不注册
// ---------------------------------------------------------------------------

func TestPartyRemoteRoomBaseNamespaceAndZeroPartyNotRegistered(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, _ := newPartyRemoteTestClient(t, server, func(settings *partyUDPRelayRemoteSettings) {
		settings.roomBase = 500000
	})
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)

	// partyID=0 ⇒ 不注册：不发 SYNC、返回 false（单人不成房）。
	if client.Sync(0, partyRemoteTestBindings(300, 5)) {
		t.Fatal("Sync(partyID=0) must not register a room")
	}
	if client.Sync(3, partyRemoteTestBindings(300)) {
		t.Fatal("Sync must refuse a one-member room")
	}
	time.Sleep(80 * time.Millisecond)
	if frames := server.syncSnapshot(); len(frames) != 0 {
		t.Fatalf("partyID=0/单人 都不该发 SYNC，实际收到 %d 帧", len(frames))
	}

	if !client.Sync(3, partyRemoteTestBindings(41, 42)) {
		t.Fatal("Sync refused the two-member room")
	}
	frames := server.syncSnapshot()
	if len(frames) == 0 {
		t.Fatal("fake relay received no SYNC frame")
	}
	if want := uint32(500003); frames[0].roomID != want {
		t.Fatalf("room_id = %d, want %d (显式 ROOM_BASE + partyID)", frames[0].roomID, want)
	}
	if client.baseRoomID() != 500000 {
		t.Fatalf("client room base = %d, want 500000", client.baseRoomID())
	}
	if _, _, ok := client.Endpoint(3, 41, 42); !ok {
		t.Fatal("Endpoint missing for the namespaced room")
	}
}

// ---------------------------------------------------------------------------
// ④ SYNC 超时 ⇒ 不可用 + 降级窗口不重复阻塞
// ---------------------------------------------------------------------------

func TestPartyRemoteSyncTimeoutFallsBackWithoutBlockingAgain(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, events := newPartyRemoteTestClient(t, server, func(settings *partyUDPRelayRemoteSettings) {
		settings.syncTimeout = 120 * time.Millisecond
	})
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	server.setNoReply(true)

	started := time.Now()
	if client.Sync(9, partyRemoteTestBindings(1, 2)) {
		t.Fatal("Sync must fail when the relay never answers")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Sync blocked %s, want ≤ syncTimeout", elapsed)
	}
	if _, _, ok := client.Endpoint(9, 1, 2); ok {
		t.Fatal("Endpoint must be unavailable after a SYNC timeout")
	}
	if !events.contains(partyUDPRelayRemoteEventUnavailable) {
		t.Fatalf("events = %v, want %s", events.names(), partyUDPRelayRemoteEventUnavailable)
	}
	if reason := events.firstField(partyUDPRelayRemoteEventUnavailable, "reason"); reason != partyUDPRelayRemoteReasonSyncTimeout {
		t.Fatalf("unavailable reason = %q, want %q", reason, partyUDPRelayRemoteReasonSyncTimeout)
	}
	if fallback := events.firstField(partyUDPRelayRemoteEventUnavailable, "fallback"); fallback != "direct_peer_list" {
		t.Fatalf("unavailable fallback = %q, want direct_peer_list", fallback)
	}

	framesBefore := len(server.syncSnapshot())
	second := time.Now()
	if client.Sync(9, partyRemoteTestBindings(1, 2)) {
		t.Fatal("Sync must stay unavailable inside the degraded window")
	}
	if elapsed := time.Since(second); elapsed > 60*time.Millisecond {
		t.Fatalf("degraded Sync blocked %s, want an immediate false", elapsed)
	}
	if framesAfter := len(server.syncSnapshot()); framesAfter != framesBefore {
		t.Fatalf("degraded window must not send another SYNC (before=%d after=%d)", framesBefore, framesAfter)
	}
}

// ---------------------------------------------------------------------------
// ⑤ 续租 / CLOSE / 重连补齐
// ---------------------------------------------------------------------------

func TestPartyRemoteRenewalKeepsLeaseAndMatrix(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, _ := newPartyRemoteTestClient(t, server, nil)
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	if !client.Sync(11, partyRemoteTestBindings(7, 8)) {
		t.Fatal("Sync refused the room")
	}
	partyRemoteTestWaitFor(t, 2*time.Second, "renewal SYNC", func() bool {
		return len(server.syncSnapshot()) >= 2
	})
	for _, frame := range server.syncSnapshot() {
		if frame.roomID != uint32(defaultPartyUDPRelayRemoteRoomBase+11) {
			t.Fatalf("renewal room_id = %d, want %d", frame.roomID, defaultPartyUDPRelayRemoteRoomBase+11)
		}
		if len(frame.members) != 2 {
			t.Fatalf("renewal members = %v, want 2 keys", frame.members)
		}
	}
	if _, _, ok := client.Endpoint(11, 7, 8); !ok {
		t.Fatal("renewal must keep the published matrix")
	}
}

func TestPartyRemoteCloseRoomSendsCloseFrame(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, events := newPartyRemoteTestClient(t, server, nil)
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	if !client.Sync(12, partyRemoteTestBindings(21, 22)) {
		t.Fatal("Sync refused the room")
	}
	client.CloseParty(12)
	partyRemoteTestWaitFor(t, 2*time.Second, "CLOSE frame", func() bool {
		return len(server.closeSnapshot()) > 0
	})
	payload := server.closeSnapshot()[0]
	if len(payload) != 5 || payload[0] != 0 {
		t.Fatalf("CLOSE payload = % X, want u8 scope=0 + u32 roomId", payload)
	}
	if roomID := binary.BigEndian.Uint32(payload[1:5]); roomID != uint32(defaultPartyUDPRelayRemoteRoomBase+12) {
		t.Fatalf("CLOSE room_id = %d, want %d", roomID, defaultPartyUDPRelayRemoteRoomBase+12)
	}
	if _, _, ok := client.Endpoint(12, 21, 22); ok {
		t.Fatal("Endpoint must be gone after CloseParty")
	}
	if cached := client.cachedRoomCount(); cached != 0 {
		t.Fatalf("cached rooms = %d, want 0 after CloseParty", cached)
	}
	if !events.contains(partyUDPRelayRemoteEventRoomClosed) {
		t.Fatalf("events = %v, want %s", events.names(), partyUDPRelayRemoteEventRoomClosed)
	}
}

func TestPartyRemoteReconnectResyncsActiveRoomsImmediately(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, events := newPartyRemoteTestClient(t, server, func(settings *partyUDPRelayRemoteSettings) {
		// 续租周期拉长：这样"断线后的第二次 SYNC"只可能来自重连补齐，而不是周期续租。
		settings.refreshEvery = 10 * time.Second
	})
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	if !client.Sync(13, partyRemoteTestBindings(31, 32)) {
		t.Fatal("Sync refused the room")
	}
	firstConn := server.syncSnapshot()[0].conn

	server.dropConnections()
	partyRemoteTestWaitFor(t, 3*time.Second, "reconnected and re-synced", func() bool {
		for _, frame := range server.syncSnapshot() {
			if frame.conn > firstConn && frame.roomID == uint32(defaultPartyUDPRelayRemoteRoomBase+13) {
				return true
			}
		}
		return false
	})
	if server.connectionCount() < 2 {
		t.Fatalf("control connections = %d, want ≥2 after a drop", server.connectionCount())
	}
	if !events.contains(partyUDPRelayRemoteEventChannelLost) {
		t.Fatalf("events = %v, want %s", events.names(), partyUDPRelayRemoteEventChannelLost)
	}
	partyRemoteTestWaitFor(t, 2*time.Second, "matrix republished after reconnect", func() bool {
		_, _, ok := client.Endpoint(13, 31, 32)
		return ok
	})
}

// ---------------------------------------------------------------------------
// ⑦ 密钥绝不进日志/事件
// ---------------------------------------------------------------------------

func TestPartyRemoteSecretNeverAppearsInEvents(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, events := newPartyRemoteTestClient(t, server, nil)
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	if !client.Sync(14, partyRemoteTestBindings(51, 52)) {
		t.Fatal("Sync refused the room")
	}
	client.CloseParty(14)
	server.dropConnections() // 触发 channel-lost / 重连路径的日志
	time.Sleep(120 * time.Millisecond)

	text := events.text()
	if strings.Contains(text, partyRemoteTestSecret) {
		t.Fatalf("密钥出现在了事件里（必须绝不出现）:\n%s", text)
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, strings.ToLower(partyRemoteTestSecret[:16])) {
		t.Fatalf("密钥前缀出现在了事件里:\n%s", text)
	}
	if !events.contains(partyUDPRelayRemoteEventConnected) {
		t.Fatalf("events = %v, want %s", events.names(), partyUDPRelayRemoteEventConnected)
	}
	if secretState := events.firstField(partyUDPRelayRemoteEventConnected, "secret"); secretState != partyUDPRelayRemoteSecretConfigured {
		t.Fatalf("connected event secret 字段 = %q, want %q（只报状态，不报内容）",
			secretState, partyUDPRelayRemoteSecretConfigured)
	}
}

// ---------------------------------------------------------------------------
// 开关解析 + 未配/非法密钥回退 embedded（options/service 侧）
// ---------------------------------------------------------------------------

func TestPartyUDPRelayModeAndRemoteConfigParsing(t *testing.T) {
	if mode := readPartyUDPRelayMode(""); mode != partyUDPRelayModeRemote {
		t.Fatalf("默认档 = %v, want remote（厂商架构）", mode)
	}
	if mode := readPartyUDPRelayMode("embedded"); mode != partyUDPRelayModeEmbedded {
		t.Fatalf("embedded 档 = %v, want embedded", mode)
	}
	if mode := readPartyUDPRelayMode(" EMBEDDED "); mode != partyUDPRelayModeEmbedded {
		t.Fatalf("大小写/空白不敏感 档 = %v, want embedded", mode)
	}
	if mode := readPartyUDPRelayMode("garbage"); mode != partyUDPRelayModeRemote {
		t.Fatalf("非法值 = %v, want remote（回默认档）", mode)
	}
	if mode := (partyUDPRelayMode(0)); mode.String() != partyUDPRelayModeValueEmbedded {
		t.Fatalf("零值 = %s, want embedded（零值必须是改动前行为）", mode)
	}

	if state := partyUDPRelayRemoteSecretState(""); state != partyUDPRelayRemoteSecretMissing {
		t.Fatalf("空密钥状态 = %q, want missing", state)
	}
	if state := partyUDPRelayRemoteSecretState("abcd"); state != partyUDPRelayRemoteSecretInvalid {
		t.Fatalf("短密钥状态 = %q, want invalid", state)
	}
	if state := partyUDPRelayRemoteSecretState(strings.Repeat("zz", 32)); state != partyUDPRelayRemoteSecretInvalid {
		t.Fatalf("非 hex 密钥状态 = %q, want invalid", state)
	}
	if state := partyUDPRelayRemoteSecretState(partyRemoteTestSecret); state != partyUDPRelayRemoteSecretConfigured {
		t.Fatalf("合法密钥状态 = %q, want configured", state)
	}
	parsed, state := readPartyUDPRelayRemoteSecret(partyRemoteTestSecret)
	if state != partyUDPRelayRemoteSecretConfigured || len(parsed) != 32 {
		t.Fatalf("readPartyUDPRelayRemoteSecret = (%d bytes, %q), want (32, configured)", len(parsed), state)
	}
	if secret, state := readPartyUDPRelayRemoteSecret(""); secret != nil || state != partyUDPRelayRemoteSecretMissing {
		t.Fatalf("未配密钥 = (%v, %q), want (nil, missing)", secret, state)
	}
	if secret, _ := readPartyUDPRelayRemoteSecret(strings.Repeat("a", 63)); secret != nil {
		t.Fatal("63 个 hex 字符必须判非法（厂商要求恰好 64）")
	}

	if base := partyUDPRelayRemoteRoomBase(""); base != defaultPartyUDPRelayRemoteRoomBase {
		t.Fatalf("默认 ROOM_BASE = %d, want %d", base, defaultPartyUDPRelayRemoteRoomBase)
	}
	if base := partyUDPRelayRemoteRoomBase("700000"); base != 700000 {
		t.Fatalf("显式 ROOM_BASE = %d, want 700000", base)
	}
	for _, invalid := range []string{"0", "-5", "abc", "99999999999999"} {
		if base := partyUDPRelayRemoteRoomBase(invalid); base != defaultPartyUDPRelayRemoteRoomBase {
			t.Fatalf("非法 ROOM_BASE %q = %d, want 默认 %d", invalid, base, defaultPartyUDPRelayRemoteRoomBase)
		}
	}
}

// TestPartyRelayRemotePlanReasons 覆盖档位决议的每一条分支：总开关关要给**准确**原因
// （relay_disabled，而不是伪装成配置不全）、显式 embedded、坏端点、拿不到具体 IPv4，
// 以及"端点是域名但 DNFBRIDGE_SERVER_IP 是具体 IPv4 ⇒ 公网 IP 用它"。
func TestPartyRelayRemotePlanReasons(t *testing.T) {
	newService := func() *Service {
		return &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	}
	ready := func(service *Service) {
		service.options.partyUDPRelayEnabled = true
		service.options.partyUDPRelayMode = partyUDPRelayModeRemote
		service.options.partyUDPRelayRemoteEndpoint = "192.0.2.12:10250"
		service.options.partyUDPRelayRemoteRoomBase = defaultPartyUDPRelayRemoteRoomBase
		service.options.serverIP = "192.0.2.12"
		service.options.partyUDPRelayRemoteSecret, service.options.partyUDPRelayRemoteSecretState =
			readPartyUDPRelayRemoteSecret(partyRemoteTestSecret)
	}

	disabled := newService()
	ready(disabled)
	disabled.options.partyUDPRelayEnabled = false
	if plan := disabled.resolvePartyUDPRelayRemotePlan(); plan.reason != partyUDPRelayRemoteReasonRelayDisabled {
		t.Fatalf("总开关关 reason = %q, want %q", plan.reason, partyUDPRelayRemoteReasonRelayDisabled)
	}

	embeddedService := newService()
	ready(embeddedService)
	embeddedService.options.partyUDPRelayMode = partyUDPRelayModeEmbedded
	if plan := embeddedService.resolvePartyUDPRelayRemotePlan(); plan.effective != partyUDPRelayModeEmbedded ||
		plan.reason != partyUDPRelayRemoteReasonModeEmbedded {
		t.Fatalf("embedded 档 = (%v, %q), want (embedded, %q)",
			plan.effective, plan.reason, partyUDPRelayRemoteReasonModeEmbedded)
	}

	badEndpoint := newService()
	ready(badEndpoint)
	badEndpoint.options.partyUDPRelayRemoteEndpoint = "192.0.2.12"
	if plan := badEndpoint.resolvePartyUDPRelayRemotePlan(); plan.reason != partyUDPRelayRemoteReasonEndpointInvalid {
		t.Fatalf("坏端点 reason = %q, want %q", plan.reason, partyUDPRelayRemoteReasonEndpointInvalid)
	}

	badIP := newService()
	ready(badIP)
	badIP.options.serverIP = "0.0.0.0"
	badIP.options.partyUDPRelayRemoteEndpoint = "relay.example.com:10250"
	if plan := badIP.resolvePartyUDPRelayRemotePlan(); plan.reason != partyUDPRelayRemoteReasonPublicIPInvalid {
		t.Fatalf("拿不到具体 IPv4 时 reason = %q, want %q", plan.reason, partyUDPRelayRemoteReasonPublicIPInvalid)
	}

	byAdvertise := newService()
	ready(byAdvertise)
	byAdvertise.options.partyUDPRelayRemoteEndpoint = "relay.example.com:10250"
	if plan := byAdvertise.resolvePartyUDPRelayRemotePlan(); plan.effective != partyUDPRelayModeRemote ||
		plan.settings.publicIP != [4]byte{192, 0, 2, 12} {
		t.Fatalf("域名端点 + SERVER_IP 回退 = (%v, % X), want (remote, 192.0.2.12)",
			plan.effective, plan.settings.publicIP)
	}

	// 厂商口径：**显式 publicIp（= DNFBRIDGE_SERVER_IP）优先**，控制端点主机只作回落。
	// 这条正是本地联调/同机部署时最容易写错的格子：端点指向回环地址时，op11 里仍然必须是
	// 公网可达的那个 IP。
	loopbackEndpoint := newService()
	ready(loopbackEndpoint)
	loopbackEndpoint.options.partyUDPRelayRemoteEndpoint = "127.0.0.1:10250"
	if plan := loopbackEndpoint.resolvePartyUDPRelayRemotePlan(); plan.settings.publicIP != [4]byte{192, 0, 2, 12} {
		t.Fatalf("回环端点 public ip = % X, want 192.0.2.12（配置的 publicIp 优先）", plan.settings.publicIP)
	}
}

// TestPartyRelayRemoteBackendWiringUsesControlChannelOnly 是**接线级**用例：
// remote 档下 Service 只建控制通道客户端、**不建内嵌中继**（因此本进程不会绑定任何
// 31000+ 的 UDP 腿，这正是现场"每场只有 1 个握手、0 转发"那条自建路径的反面），
// 并且 `partyRelayBackend()` 返回的正是它、`Sync`+`Endpoint` 走通真矩阵。
// 端点指向本地假中继，全程不碰真中继。
func TestPartyRelayRemoteBackendWiringUsesControlChannelOnly(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	host, port := server.hostPort()

	service := &Service{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	service.options.partyUDPRelayEnabled = true
	service.options.partyUDPRelayMode = partyUDPRelayModeRemote
	service.options.partyUDPRelayRemoteEndpoint = fmt.Sprintf("%s:%d", host, port)
	service.options.partyUDPRelayRemoteRoomBase = defaultPartyUDPRelayRemoteRoomBase
	service.options.serverIP = "192.0.2.12"
	service.options.partyUDPRelayRemoteSecret, service.options.partyUDPRelayRemoteSecretState =
		readPartyUDPRelayRemoteSecret(partyRemoteTestSecret)

	remote, embedded, err := service.newPartyUDPRelayBackends()
	if err != nil {
		t.Fatalf("newPartyUDPRelayBackends: %v", err)
	}
	t.Cleanup(func() {
		if remote != nil {
			_ = remote.Close()
		}
		if embedded != nil {
			_ = embedded.Close()
		}
	})
	if remote == nil {
		t.Fatal("remote 档必须建控制通道客户端")
	}
	if embedded != nil {
		t.Fatal("remote 档必须**不**建内嵌中继（否则会绑定自建 UDP 腿，正是要修掉的那条路）")
	}
	if service.partyUDPRelay != nil {
		t.Fatal("remote 档下 Service.partyUDPRelay 必须为 nil")
	}

	// 接线：把后端挂进 Service（Start 里就是这样赋值的），走 partyRelayBackend 取用。
	service.partyUDPRelayRemote = remote
	backend := service.partyRelayBackend()
	if backend == nil {
		t.Fatal("partyRelayBackend must return the remote backend")
	}
	if mode := service.partyUDPRelayEffectiveMode(); mode != partyUDPRelayModeRemote {
		t.Fatalf("effective mode = %v, want remote", mode)
	}
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", remote.connectedState)
	partyRemoteTestWaitFor(t, 2*time.Second, "HELLO from the wired client", func() bool {
		return len(server.helloPayloads()) > 0
	})

	if !backend.Sync(5, partyRemoteTestBindings(101, 202)) {
		t.Fatal("backend.Sync refused the room")
	}
	address, portAB, ok := backend.Endpoint(5, 101, 202)
	if !ok {
		t.Fatal("backend.Endpoint(101->202) missing")
	}
	if address != [4]byte{192, 0, 2, 12} {
		t.Fatalf("relay ip = % X, want 192.0.2.12", address)
	}
	wantAB, _ := partyRemoteTestLegPort([]uint16{101, 202}, 101, 202)
	if portAB != wantAB {
		t.Fatalf("backend.Endpoint(101->202) port = %d, want %d", portAB, wantAB)
	}
	if frames := server.syncSnapshot(); len(frames) == 0 ||
		frames[0].roomID != uint32(defaultPartyUDPRelayRemoteRoomBase+5) {
		t.Fatalf("SYNC frames = %+v, want room_id %d", frames, defaultPartyUDPRelayRemoteRoomBase+5)
	}
	// 收尾：Close 必须把租约交还掉（CLOSE 帧）。
	if err := service.closePartyUDPRelayBackends(); err != nil {
		t.Fatalf("closePartyUDPRelayBackends: %v", err)
	}
	partyRemoteTestWaitFor(t, 2*time.Second, "CLOSE frame on shutdown", func() bool {
		return len(server.closeSnapshot()) > 0
	})
	if backend := service.partyRelayBackend(); backend != nil {
		t.Fatal("backend must be nil after closing everything")
	}
}

// TestPartyRelayRemoteSwitchFallsBackToEmbedded 钉住"未配/非法密钥 ⇒ 记具名事件 + 回退
// embedded，且启动不失败、日志里出现密钥状态词而不是密钥内容"。
func TestPartyRelayRemoteSwitchFallsBackToEmbedded(t *testing.T) {
	cases := []struct {
		name          string
		secret        string
		secretState   string
		wantReason    string
		wantEffective partyUDPRelayMode
	}{
		{
			name:          "未配密钥",
			secret:        "",
			secretState:   partyUDPRelayRemoteSecretMissing,
			wantReason:    partyUDPRelayRemoteReasonSecretMissing,
			wantEffective: partyUDPRelayModeEmbedded,
		},
		{
			name:          "密钥格式非法",
			secret:        "not-a-secret",
			secretState:   partyUDPRelayRemoteSecretInvalid,
			wantReason:    partyUDPRelayRemoteReasonSecretInvalid,
			wantEffective: partyUDPRelayModeEmbedded,
		},
		{
			name:          "密钥齐备 ⇒ remote 生效",
			secret:        partyRemoteTestSecret,
			secretState:   partyUDPRelayRemoteSecretConfigured,
			wantEffective: partyUDPRelayModeRemote,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			service := &Service{
				logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}
			service.options.partyUDPRelayEnabled = true
			service.options.partyUDPRelayMode = partyUDPRelayModeRemote
			service.options.partyUDPRelayRemoteEndpoint = "192.0.2.12:10250"
			service.options.partyUDPRelayRemoteRoomBase = defaultPartyUDPRelayRemoteRoomBase
			service.options.serverIP = "192.0.2.12"
			service.options.partyUDPRelayRemoteSecret, service.options.partyUDPRelayRemoteSecretState =
				readPartyUDPRelayRemoteSecret(testCase.secret)

			plan := service.resolvePartyUDPRelayRemotePlan()
			if plan.effective != testCase.wantEffective {
				t.Fatalf("effective = %v, want %v (reason=%q)", plan.effective, testCase.wantEffective, plan.reason)
			}
			if testCase.wantReason != "" && plan.reason != testCase.wantReason {
				t.Fatalf("reason = %q, want %q", plan.reason, testCase.wantReason)
			}
			if testCase.wantEffective == partyUDPRelayModeRemote {
				if plan.settings.publicIP != [4]byte{192, 0, 2, 12} {
					t.Fatalf("public ip = % X, want 192.0.2.12", plan.settings.publicIP)
				}
				if plan.settings.roomBase != defaultPartyUDPRelayRemoteRoomBase {
					t.Fatalf("room base = %d, want %d", plan.settings.roomBase, defaultPartyUDPRelayRemoteRoomBase)
				}
				if len(plan.settings.secret) != 32 {
					t.Fatalf("secret bytes = %d, want 32", len(plan.settings.secret))
				}
				if plan.reason != "" {
					t.Fatalf("remote 生效时 reason = %q, want 空", plan.reason)
				}
				return
			}
			// 回退档：必须两条具名事件，一条讲原因，一条讲"已回退 embedded"。
			service.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventUnavailable,
				"reason", plan.reason,
				"fallback", "embedded_relay",
				"endpoint", service.options.partyUDPRelayRemoteEndpoint,
				"secret", service.options.partyUDPRelayRemoteSecretState)
			service.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventEmbeddedFallback,
				"reason", plan.reason,
				"mode_requested", plan.requested.String(),
				"mode_effective", plan.effective.String())
			text := logs.String()
			if !strings.Contains(text, partyUDPRelayRemoteEventUnavailable) {
				t.Fatalf("缺少 %s 事件:\n%s", partyUDPRelayRemoteEventUnavailable, text)
			}
			if !strings.Contains(text, partyUDPRelayRemoteEventEmbeddedFallback) {
				t.Fatalf("缺少 %s 事件:\n%s", partyUDPRelayRemoteEventEmbeddedFallback, text)
			}
			if !strings.Contains(text, "reason="+testCase.wantReason) {
				t.Fatalf("缺少 reason=%s:\n%s", testCase.wantReason, text)
			}
			if testCase.secret != "" && strings.Contains(text, testCase.secret) {
				t.Fatalf("密钥出现在日志里（必须绝不出现）:\n%s", text)
			}
		})
	}
}

// TestPartyRelayRemoteBackendSelectionKeepsEmbeddedFallback 钉住"后端选择"：
// 没有 remote 客户端时 partyRelayBackend() 仍然返回内嵌 relay（零值 options 的测试路径
// 与线上回退档都走这条），把内嵌 relay 关掉则两个都没有。
func TestPartyRelayRemoteBackendSelectionKeepsEmbeddedFallback(t *testing.T) {
	service := &Service{}
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatalf("new embedded relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	service.partyUDPRelay = relay
	if backend := service.partyRelayBackend(); backend == nil {
		t.Fatal("embedded relay must remain the backend when no remote client exists")
	}
	if mode := service.partyUDPRelayEffectiveMode(); mode != partyUDPRelayModeEmbedded {
		t.Fatalf("effective mode = %v, want embedded", mode)
	}
	if err := service.closePartyUDPRelayBackends(); err != nil {
		t.Fatalf("close backends: %v", err)
	}
	if backend := service.partyRelayBackend(); backend != nil {
		t.Fatal("backend must be nil after closing the embedded relay")
	}
}

// TestPartyRelayRemoteUnavailableEventIsThrottled 钉住"失败事件不刷屏"：
// 同一个 reason 在限流窗口内只记一条。
func TestPartyRelayRemoteUnavailableEventIsThrottled(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	client, events := newPartyRemoteTestClient(t, server, func(settings *partyUDPRelayRemoteSettings) {
		settings.syncTimeout = 60 * time.Millisecond
	})
	partyRemoteTestWaitFor(t, 2*time.Second, "control channel connected", client.connectedState)
	server.setNoReply(true)

	for attempt := 0; attempt < 3; attempt++ {
		if client.Sync(15, partyRemoteTestBindings(61, 62)) {
			t.Fatal("Sync must fail when the relay never answers")
		}
		time.Sleep(80 * time.Millisecond) // 越过降级窗口，逼出第二次真 SYNC 与第二次超时
	}
	if got := events.count(partyUDPRelayRemoteEventUnavailable); got != 1 {
		t.Fatalf("unavailable 事件 = %d 条, want 1（同一 reason 限流 10s）", got)
	}
	if !server.noReplyEnabled() {
		t.Fatal("fake relay should still be in no-reply mode")
	}
}

// TestPartyRemoteHelloRejectedMarksUnavailable 钉住认证失败路径：
// 中继不回 HELLO_OK（或回错 opcode）时，客户端**不崩、不阻塞**，记一条
// `party-udp-relay-remote-unavailable`（reason=hello_rejected），并继续按退避重连。
func TestPartyRemoteHelloRejectedMarksUnavailable(t *testing.T) {
	secret, _ := hex.DecodeString(partyRemoteTestSecret)
	server := newPartyRemoteTestServer(t, secret)
	server.setHelloRejected(true)
	client, events := newPartyRemoteTestClient(t, server, nil)

	partyRemoteTestWaitFor(t, 2*time.Second, "hello_rejected event", func() bool {
		return events.firstField(partyUDPRelayRemoteEventUnavailable, "reason") == partyUDPRelayRemoteReasonHelloRejected
	})
	if client.connectedState() {
		t.Fatal("client must not report connected when HELLO was rejected")
	}
	if client.Sync(21, partyRemoteTestBindings(71, 72)) {
		t.Fatal("Sync must fail while the control channel is not authenticated")
	}
	if _, _, ok := client.Endpoint(21, 71, 72); ok {
		t.Fatal("Endpoint must be unavailable without a matrix")
	}
	// 仍然在重连（不是放弃）：HELLO 会被反复尝试。
	partyRemoteTestWaitFor(t, 3*time.Second, "reconnect attempts", func() bool {
		return len(server.helloPayloads()) >= 2
	})
}
