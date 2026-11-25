package partyudprelay

import (
	"bytes"
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

const testSecretHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func testSecretBytes(t *testing.T) []byte {
	t.Helper()
	b, state := ParseSecretHex(testSecretHex)
	if state != "configured" || len(b) != SecretByteLength {
		t.Fatalf("ParseSecretHex failed: state=%s len=%d", state, len(b))
	}
	return b
}

// findFreeUDPPortRange finds a contiguous block of count free IPv4 UDP ports on 127.0.0.1.
func findFreeUDPPortRange(t *testing.T, count int) (int, int) {
	t.Helper()
	for base := 41000; base+count <= 58000; base += count + 7 {
		conns := make([]*net.UDPConn, 0, count)
		ok := true
		for p := base; p < base+count; p++ {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
			if err != nil {
				ok = false
				break
			}
			conns = append(conns, c)
		}
		for _, c := range conns {
			_ = c.Close()
		}
		if ok {
			return base, base + count - 1
		}
	}
	t.Fatalf("unable to find %d contiguous free UDP ports", count)
	return 0, 0
}

func dialAuthenticatedControl(t *testing.T, addr string, secret []byte) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hello := make([]byte, 1+32)
	hello[0] = ProtocolVersion
	copy(hello[1:], computeHelloMAC(secret))
	if err := writeFrame(conn, OpHello, hello, 2*time.Second); err != nil {
		t.Fatalf("write HELLO: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, payload, err := readFrame(conn)
	if err != nil || op != OpHelloOK || len(payload) != 0 {
		t.Fatalf("HELLO response mismatch: op=%#x len=%d err=%v", op, len(payload), err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func buildSyncFramePayload(scope byte, roomID uint32, mode byte, members []uint16) []byte {
	p := make([]byte, SyncHeaderBytes+len(members)*2)
	p[0] = scope
	binary.BigEndian.PutUint32(p[1:5], roomID)
	p[5] = mode
	binary.BigEndian.PutUint16(p[6:8], uint16(len(members)))
	for i, m := range members {
		binary.BigEndian.PutUint16(p[SyncHeaderBytes+i*2:SyncHeaderBytes+i*2+2], m)
	}
	return p
}

func decodeSyncOKMap(t *testing.T, payload []byte) (uint32, map[legPair]uint16) {
	t.Helper()
	if len(payload) < 6 {
		t.Fatalf("short SYNC_OK: %d", len(payload))
	}
	roomID := binary.BigEndian.Uint32(payload[0:4])
	count := int(binary.BigEndian.Uint16(payload[4:6]))
	if len(payload) != 6+count*6 {
		t.Fatalf("malformed SYNC_OK: len=%d count=%d", len(payload), count)
	}
	out := make(map[legPair]uint16, count)
	for i := 0; i < count; i++ {
		off := 6 + i*6
		pair := legPair{
			owner: binary.BigEndian.Uint16(payload[off : off+2]),
			peer:  binary.BigEndian.Uint16(payload[off+2 : off+4]),
		}
		out[pair] = binary.BigEndian.Uint16(payload[off+4 : off+6])
	}
	return roomID, out
}

func TestRelayHelloAuthAcceptAndReject(t *testing.T) {
	if _, state := ParseSecretHex(""); state != "missing" {
		t.Fatalf("empty secret state=%q, want missing", state)
	}
	if _, state := ParseSecretHex("not-hex"); state != "invalid" {
		t.Fatalf("short secret state=%q, want invalid", state)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	secret := testSecretBytes(t)
	portMin, portMax := findFreeUDPPortRange(t, 16)
	srv, err := Start(Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Wrong secret rejected by closing connection.
	wrongSecret, _ := hex.DecodeString("ff112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	connWrong, err := net.DialTimeout("tcp", srv.ControlAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial wrong: %v", err)
	}
	badHello := append([]byte{ProtocolVersion}, computeHelloMAC(wrongSecret)...)
	_ = writeFrame(connWrong, OpHello, badHello, time.Second)
	_ = connWrong.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := readFrame(connWrong); err == nil {
		t.Fatal("expected connection close on wrong HMAC")
	}
	_ = connWrong.Close()

	// Wrong protocol version rejected.
	connVer, err := net.DialTimeout("tcp", srv.ControlAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial wrong ver: %v", err)
	}
	verHello := append([]byte{1}, computeHelloMAC(secret)...)
	_ = writeFrame(connVer, OpHello, verHello, time.Second)
	_ = connVer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := readFrame(connVer); err == nil {
		t.Fatal("expected connection close on wrong protocol version")
	}
	_ = connVer.Close()

	// Valid HELLO accepted.
	connOK := dialAuthenticatedControl(t, srv.ControlAddr(), secret)
	_ = connOK.Close()

	if stats := srv.Stats(); stats.HelloAccepted != 1 || stats.HelloRejected < 2 {
		t.Fatalf("unexpected hello counters: %+v", stats)
	}
	if strings.Contains(logBuf.String(), testSecretHex) {
		t.Fatal("secret leaked into logs")
	}
}

func TestRelayFourPlayerTwelveLegsBidirectionalSymmetricNATAndSpoofReject(t *testing.T) {
	secret := testSecretBytes(t)
	portMin, portMax := findFreeUDPPortRange(t, 24)
	srv, err := Start(Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctrl := dialAuthenticatedControl(t, srv.ControlAddr(), secret)
	members := []uint16{101, 102, 103, 104}
	if err := writeFrame(ctrl, OpSync, buildSyncFramePayload(ScopeParty, 100001, SyncModeMembers, members), 2*time.Second); err != nil {
		t.Fatalf("write SYNC: %v", err)
	}
	_ = ctrl.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, payload, err := readFrame(ctrl)
	if err != nil || op != OpSyncOK {
		t.Fatalf("SYNC reply: op=%#x err=%v", op, err)
	}
	roomID, ports := decodeSyncOKMap(t, payload)
	if roomID != 100001 || len(ports) != 12 {
		t.Fatalf("expected room 100001 with 12 directed legs, got room=%d legs=%d", roomID, len(ports))
	}

	// Open 4 loopback player UDP sockets.
	players := make(map[uint16]*net.UDPConn, len(members))
	for _, m := range members {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen player %d: %v", m, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		players[m] = c
	}

	send := func(conn *net.UDPConn, port uint16, msg string) {
		t.Helper()
		if _, err := conn.WriteToUDP([]byte(msg), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}); err != nil {
			t.Fatalf("send %q -> %d: %v", msg, port, err)
		}
	}
	recv := func(conn *net.UDPConn) (string, int) {
		t.Helper()
		buf := make([]byte, 256)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		return string(buf[:n]), src.Port
	}

	// 1) Initial datagram buffering: 101 sends to 102 before 102 sends anything.
	send(players[101], ports[legPair{owner: 101, peer: 102}], "early-101-to-102")
	time.Sleep(25 * time.Millisecond)
	send(players[102], ports[legPair{owner: 102, peer: 101}], "hello-102-to-101")

	got102, srcPort102 := recv(players[102])
	if got102 != "early-101-to-102" || uint16(srcPort102) != ports[legPair{owner: 102, peer: 101}] {
		t.Fatalf("102 expected flushed early packet from port %d, got %q from %d", ports[legPair{owner: 102, peer: 101}], got102, srcPort102)
	}
	got101, srcPort101 := recv(players[101])
	if got101 != "hello-102-to-101" || uint16(srcPort101) != ports[legPair{owner: 101, peer: 102}] {
		t.Fatalf("101 expected hello from port %d, got %q from %d", ports[legPair{owner: 101, peer: 102}], got101, srcPort101)
	}

	// 2) Exercise all remaining directed legs among all 4 players (total 12 directed legs).
	for _, owner := range members {
		for _, peer := range members {
			if owner == peer || (owner == 101 && peer == 102) || (owner == 102 && peer == 101) {
				continue
			}
			send(players[owner], ports[legPair{owner: owner, peer: peer}], fmt.Sprintf("seed-%d-%d", owner, peer))
		}
	}
	for _, receiver := range members {
		expected := make(map[string]uint16)
		for _, sender := range members {
			if sender == receiver || (sender == 101 && receiver == 102) || (sender == 102 && receiver == 101) {
				continue
			}
			expected[fmt.Sprintf("seed-%d-%d", sender, receiver)] = ports[legPair{owner: receiver, peer: sender}]
		}
		for len(expected) > 0 {
			msg, fromPort := recv(players[receiver])
			wantPort, ok := expected[msg]
			if !ok {
				t.Fatalf("player %d got unexpected packet %q from %d", receiver, msg, fromPort)
			}
			if uint16(fromPort) != wantPort {
				t.Fatalf("player %d packet %q source port=%d, want %d", receiver, msg, fromPort, wantPort)
			}
			delete(expected, msg)
		}
	}

	// 3) Symmetric NAT port relearn: player 101 switches to a new UDP port on the same 127.0.0.1 IPv4.
	rebound101, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen rebound 101: %v", err)
	}
	defer func() { _ = rebound101.Close() }()
	send(rebound101, ports[legPair{owner: 101, peer: 102}], "rebound-101-to-102")
	gotRebound, _ := recv(players[102])
	if gotRebound != "rebound-101-to-102" {
		t.Fatalf("expected rebound packet, got %q", gotRebound)
	}
	send(players[102], ports[legPair{owner: 102, peer: 101}], "reply-to-rebound-101")
	gotBack, backPort := recv(rebound101)
	if gotBack != "reply-to-rebound-101" || uint16(backPort) != ports[legPair{owner: 101, peer: 102}] {
		t.Fatalf("rebound 101 expected reply from %d, got %q from %d", ports[legPair{owner: 101, peer: 102}], gotBack, backPort)
	}

	// 4) Spoofed different IPv4 rejected (127.0.0.2 -> leg (101,102) bound to 127.0.0.1).
	if spoofConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 0}); err == nil {
		defer func() { _ = spoofConn.Close() }()
		send(spoofConn, ports[legPair{owner: 101, peer: 102}], "spoofed-packet")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && srv.Stats().RejectedSource == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		if srv.Stats().RejectedSource == 0 {
			t.Fatal("expected spoofed IPv4 datagram to be rejected as source_mismatch")
		}
		// Ensure player 102 did not receive spoofed payload.
		_ = players[102].SetReadDeadline(time.Now().Add(40 * time.Millisecond))
		buf := make([]byte, 64)
		if n, _, err := players[102].ReadFromUDP(buf); err == nil {
			t.Fatalf("spoofed packet leaked to peer 102: %q", string(buf[:n]))
		}
	}
}

func TestRelayRateLimitAndDeadlineCache(t *testing.T) {
	secret := testSecretBytes(t)
	portMin, portMax := findFreeUDPPortRange(t, 14)
	srv, err := Start(Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
		MaxPPS:        10,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	allocs, failReason := srv.syncRoom(ScopeParty, 200001, []uint16{11, 22})
	if failReason != 0 || len(allocs) != 2 {
		t.Fatalf("syncRoom failed: reason=%d len=%d", failReason, len(allocs))
	}
	var port11to22, port22to11 uint16
	for _, a := range allocs {
		if a.owner == 11 && a.peer == 22 {
			port11to22 = a.port
		}
		if a.owner == 22 && a.peer == 11 {
			port22to11 = a.port
		}
	}
	p11, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p11.Close() }()
	p22, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p22.Close() }()

	// Seed opposite leg 22->11 so 22's endpoint is learned.
	_, _ = p22.WriteToUDP([]byte("seed"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port22to11)})
	time.Sleep(20 * time.Millisecond)

	// Burst 30 packets from 11 -> 22; maxPPS=10 must rate-limit at least 15.
	for i := 0; i < 30; i++ {
		_, _ = p11.WriteToUDP([]byte{byte(i + 1)}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port11to22)})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && srv.Stats().RateLimited < 15 {
		time.Sleep(5 * time.Millisecond)
	}
	if st := srv.Stats(); st.RateLimited < 15 {
		t.Fatalf("expected >=15 rate-limited packets with MaxPPS=10, got %+v", st)
	}

	// Verify write deadline caching on leg (22->11): 50 forwards reuse a single deadline.
	srv.mu.Lock()
	room := srv.rooms[roomKey{scope: ScopeParty, roomID: 200001}]
	fromLeg := room.legs[legPair{owner: 11, peer: 22}]
	toLeg := room.legs[legPair{owner: 22, peer: 11}]
	srv.mu.Unlock()

	now := time.Now().Add(time.Minute)
	var firstDeadline int64
	for i := 0; i < 50; i++ {
		srv.forwardDatagram(fromLeg, toLeg, []byte{9}, now.Add(time.Duration(i)*time.Microsecond))
		dl := toLeg.writeDeadline.Load()
		if i == 0 {
			firstDeadline = dl
		}
		if dl == 0 || dl != firstDeadline {
			t.Fatalf("burst packet %d changed cached deadline from %d to %d", i, firstDeadline, dl)
		}
	}
	srv.forwardDatagram(fromLeg, toLeg, []byte{9}, now.Add(defaultWriteTimeout*3/4))
	if toLeg.writeDeadline.Load() <= firstDeadline {
		t.Fatal("cached deadline did not refresh near expiry")
	}
}

func TestRelaySyncIdempotencyMemberChangeCooldownAndRollback(t *testing.T) {
	var clockMu sync.Mutex
	fakeNow := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		fakeNow = fakeNow.Add(d)
		clockMu.Unlock()
	}

	secret := testSecretBytes(t)
	// Exactly 12 ports total to test cooldown quarantine and port-exhaustion rollback.
	portMin, portMax := findFreeUDPPortRange(t, 12)
	srv, err := Start(Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
		LeaseTTL:      30 * time.Second,
		PartyCooldown: 60 * time.Second,
		PVPCooldown:   180 * time.Second,
		Now:           nowFn,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// 1) Sync 3-member party room (6 legs).
	allocs1, reason := srv.syncRoom(ScopeParty, 1001, []uint16{3, 1, 2, 2, 0})
	if reason != 0 || len(allocs1) != 6 {
		t.Fatalf("initial sync failed: reason=%d len=%d", reason, len(allocs1))
	}
	// Idempotent renewal keeps exact same ports.
	allocs2, reason := srv.syncRoom(ScopeParty, 1001, []uint16{1, 2, 3})
	if reason != 0 || len(allocs2) != 6 {
		t.Fatalf("idempotent sync failed: reason=%d len=%d", reason, len(allocs2))
	}
	for i := range allocs1 {
		if allocs1[i] != allocs2[i] {
			t.Fatalf("idempotent sync changed leg %d: %+v vs %+v", i, allocs1[i], allocs2[i])
		}
	}

	// 2) Shrink to 2 members (2 legs kept, 4 legs enter 60s party cooldown -> 6 free ports left).
	allocs3, reason := srv.syncRoom(ScopeParty, 1001, []uint16{1, 2})
	if reason != 0 || len(allocs3) != 2 {
		t.Fatalf("shrink sync failed: reason=%d len=%d", reason, len(allocs3))
	}
	// Verify surviving pair (1,2) kept its original port.
	orig12 := uint16(0)
	for _, a := range allocs1 {
		if a.owner == 1 && a.peer == 2 {
			orig12 = a.port
		}
	}
	if allocs3[0].port != orig12 {
		t.Fatalf("surviving leg (1,2) changed port %d -> %d", orig12, allocs3[0].port)
	}

	// 3) Sync a 3-member PVP room (6 legs -> fills remaining 6 free ports; 4 party ports still in cooldown).
	pvpAllocs, reason := srv.syncRoom(1, 2001, []uint16{10, 20, 30})
	if reason != 0 || len(pvpAllocs) != 6 {
		t.Fatalf("pvp sync failed: reason=%d len=%d", reason, len(pvpAllocs))
	}

	// 4) Pool now exhausted (2 active party + 4 quarantined party + 6 active PVP = 12).
	// Another room MUST fail with SyncFailPortExhausted and roll back cleanly without touching existing rooms.
	if _, reason := srv.syncRoom(ScopeParty, 1002, []uint16{50, 60}); reason != SyncFailPortExhausted {
		t.Fatalf("expected SyncFailPortExhausted, got reason=%d", reason)
	}
	if got := srv.ActivePortsForRoom(ScopeParty, 1001); len(got) != 2 {
		t.Fatalf("existing party room damaged after rollback: %v", got)
	}

	// 5) Close PVP room -> its 6 ports enter 180s quarantine.
	srv.closeRoom(1, 2001)
	if _, reason := srv.syncRoom(ScopeParty, 1002, []uint16{50, 60}); reason != SyncFailPortExhausted {
		t.Fatalf("expected quarantine to block immediate reuse, got reason=%d", reason)
	}

	// 6) Advance 61s (only the 4 party-cooldown ports unlock; 6 PVP ports still quarantined for 180s!).
	// Renew room 1001 first so it doesn't expire.
	advance(20 * time.Second)
	if _, reason := srv.syncRoom(ScopeParty, 1001, []uint16{1, 2}); reason != 0 {
		t.Fatalf("renew 1001 failed: %d", reason)
	}
	advance(20 * time.Second)
	if _, reason := srv.syncRoom(ScopeParty, 1001, []uint16{1, 2}); reason != 0 {
		t.Fatalf("renew 1001 failed: %d", reason)
	}
	advance(21 * time.Second) // 61s since party shrink & PVP close
	if _, reason := srv.syncRoom(ScopeParty, 1002, []uint16{50, 60, 70}); reason != SyncFailPortExhausted {
		// 6 legs needed, only 4 party ports unlocked -> MUST fail (PVP ports still quarantined!).
		t.Fatalf("PVP ports escaped 180s quarantine early: reason=%d", reason)
	}
	if allocs1002, reason := srv.syncRoom(ScopeParty, 1002, []uint16{50, 60}); reason != 0 || len(allocs1002) != 2 {
		t.Fatalf("party cooldown ports did not unlock after 61s: reason=%d len=%d", reason, len(allocs1002))
	}

	// 7) Advance 125s (total 186s since PVP close -> PVP ports unlock; party rooms 1001 & 1002 expire into 60s cooldown).
	advance(125 * time.Second)
	srv.reapExpired()
	// Right after expiry, the 4 expired party ports are in 60s cooldown while 8 ports (6 PVP + 2 unused) are free.
	if allocs1003, reason := srv.syncRoom(ScopeParty, 1003, []uint16{1, 2, 3}); reason != 0 || len(allocs1003) != 6 {
		t.Fatalf("expected 6 unlocked PVP ports available after 180s cooldown, got reason=%d len=%d", reason, len(allocs1003))
	}
	srv.closeRoom(ScopeParty, 1003)
	// Advance 61s so all party cooldowns finish -> all 12 ports available for a 4-player party.
	advance(61 * time.Second)
	if allocs1004, reason := srv.syncRoom(ScopeParty, 1004, []uint16{1, 2, 3, 4}); reason != 0 || len(allocs1004) != 12 {
		t.Fatalf("expected all 12 ports free after party 60s cooldown, got reason=%d len=%d", reason, len(allocs1004))
	}
}

func TestRelayCloseAndLeaseExpiryReaper(t *testing.T) {
	secret := testSecretBytes(t)
	portMin, portMax := findFreeUDPPortRange(t, 16)
	srv, err := Start(Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
		LeaseTTL:      90 * time.Millisecond,
		PartyCooldown: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctrl := dialAuthenticatedControl(t, srv.ControlAddr(), secret)

	// Open room 100010 and explicitly CLOSE it via wire protocol.
	if err := writeFrame(ctrl, OpSync, buildSyncFramePayload(ScopeParty, 100010, SyncModeMembers, []uint16{1, 2}), time.Second); err != nil {
		t.Fatal(err)
	}
	op, _, err := readFrame(ctrl)
	if err != nil || op != OpSyncOK {
		t.Fatalf("SYNC 100010: op=%#x err=%v", op, err)
	}
	closePayload := make([]byte, 5)
	closePayload[0] = ScopeParty
	binary.BigEndian.PutUint32(closePayload[1:5], 100010)
	if err := writeFrame(ctrl, OpClose, closePayload, time.Second); err != nil {
		t.Fatal(err)
	}
	op, closeOK, err := readFrame(ctrl)
	if err != nil || op != OpCloseOK || len(closeOK) != 4 || binary.BigEndian.Uint32(closeOK) != 100010 {
		t.Fatalf("CLOSE_OK mismatch: op=%#x payload=%x err=%v", op, closeOK, err)
	}
	if st := srv.Stats(); st.ActiveRooms != 0 || st.RoomsClosed != 1 {
		t.Fatalf("expected room closed immediately, got %+v", st)
	}

	// Open room 100020 and let its 90ms lease expire automatically via reapLoop.
	if err := writeFrame(ctrl, OpSync, buildSyncFramePayload(ScopeParty, 100020, SyncModeMembers, []uint16{3, 4}), time.Second); err != nil {
		t.Fatal(err)
	}
	op, _, err = readFrame(ctrl)
	if err != nil || op != OpSyncOK {
		t.Fatalf("SYNC 100020: op=%#x err=%v", op, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := srv.Stats(); st.ActiveRooms == 0 && st.ExpiredRooms >= 1 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if st := srv.Stats(); st.ActiveRooms != 0 || st.ActiveLegs != 0 || st.ExpiredRooms < 1 {
		t.Fatalf("expected unrenewed room to expire, got %+v", st)
	}
}
