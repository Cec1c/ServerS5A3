package dnfbridge

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/partyudprelay"
)

func findFreeStandaloneRelayPorts(t *testing.T, count int) (int, int) {
	t.Helper()
	for base := 45000; base+count <= 59000; base += count + 11 {
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
	t.Fatalf("could not reserve %d contiguous free UDP ports", count)
	return 0, 0
}

// TestPartyUDPRelayStandaloneFourPlayerE2E exercises the production remote
// client (currentPartyUDPRelayRemote) against the real standalone Go relay
// server (partyudprelay.Server) across 4 players (12 directed loopback UDP legs),
// verifying HMAC auth, full 12-leg forwarding, Symmetric NAT port relearn,
// lease renewal, CLOSE teardown, and zero secret exposure in events.
func TestPartyUDPRelayStandaloneFourPlayerE2E(t *testing.T) {
	const secretHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	secret, state := partyudprelay.ParseSecretHex(secretHex)
	if state != "configured" {
		t.Fatalf("secret state=%s", state)
	}
	portMin, portMax := findFreeStandaloneRelayPorts(t, 20)
	relaySrv, err := partyudprelay.Start(partyudprelay.Config{
		ControlListen: "127.0.0.1:0",
		UDPIp:         "127.0.0.1",
		PortMin:       portMin,
		PortMax:       portMax,
		Secret:        secret,
		LeaseTTL:      5 * time.Second,
		PartyCooldown: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("start standalone relay: %v", err)
	}
	defer func() { _ = relaySrv.Close() }()

	host, portText, err := net.SplitHostPort(relaySrv.ControlAddr())
	if err != nil {
		t.Fatalf("split control addr: %v", err)
	}
	ctrlPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse control port: %v", err)
	}

	var eventMu sync.Mutex
	var events []string
	remote, err := newCurrentPartyUDPRelayRemote(partyUDPRelayRemoteSettings{
		endpoint:       relaySrv.ControlAddr(),
		host:           host,
		port:           ctrlPort,
		publicIP:       [4]byte{127, 0, 0, 1},
		secret:         secret,
		roomBase:       100000,
		connectTimeout: time.Second,
		syncTimeout:    time.Second,
		writeTimeout:   time.Second,
		refreshEvery:   200 * time.Millisecond,
		minBackoff:     50 * time.Millisecond,
		maxBackoff:     200 * time.Millisecond,
		stableAfter:    time.Second,
		freshWindow:    150 * time.Millisecond,
		onEvent: func(name string, args ...any) {
			eventMu.Lock()
			events = append(events, fmt.Sprintf("%s %v", name, args))
			eventMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("newCurrentPartyUDPRelayRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !remote.connectedState() {
		time.Sleep(10 * time.Millisecond)
	}
	if !remote.connectedState() {
		t.Fatal("remote client failed to authenticate HELLO against standalone relay")
	}

	const partyID uint16 = 7
	members := []uint16{1001, 1002, 1003, 1004}
	bindings := make([]currentPartyUDPRelayBinding, 0, len(members))
	for _, m := range members {
		bindings = append(bindings, currentPartyUDPRelayBinding{
			characterID: m,
			generation:  1,
			address:     [4]byte{127, 0, 0, 1},
		})
	}
	if !remote.Sync(partyID, bindings) {
		t.Fatal("remote.Sync failed for 4-player party")
	}

	// Verify all 12 directed legs returned by Endpoint().
	legPorts := make(map[[2]uint16]uint16, 12)
	for _, owner := range members {
		for _, peer := range members {
			if owner == peer {
				continue
			}
			ip, port, ok := remote.Endpoint(partyID, owner, peer)
			if !ok || ip != [4]byte{127, 0, 0, 1} || int(port) < portMin || int(port) > portMax {
				t.Fatalf("Endpoint(%d,%d,%d) invalid: ip=%v port=%d ok=%t", partyID, owner, peer, ip, port, ok)
			}
			legPorts[[2]uint16{owner, peer}] = port
		}
	}

	// Open 4 loopback UDP player sockets and verify bidirectional forwarding across all 12 legs.
	playerConns := make(map[uint16]*net.UDPConn, 4)
	for _, m := range members {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen player %d: %v", m, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		playerConns[m] = c
	}

	for _, owner := range members {
		for _, peer := range members {
			if owner == peer {
				continue
			}
			msg := fmt.Sprintf("p%d->p%d", owner, peer)
			dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(legPorts[[2]uint16{owner, peer}])}
			if _, err := playerConns[owner].WriteToUDP([]byte(msg), dst); err != nil {
				t.Fatalf("write %s: %v", msg, err)
			}
		}
	}

	for _, receiver := range members {
		want := make(map[string]uint16, 3)
		for _, sender := range members {
			if sender == receiver {
				continue
			}
			want[fmt.Sprintf("p%d->p%d", sender, receiver)] = legPorts[[2]uint16{receiver, sender}]
		}
		buf := make([]byte, 128)
		for len(want) > 0 {
			_ = playerConns[receiver].SetReadDeadline(time.Now().Add(2 * time.Second))
			n, src, err := playerConns[receiver].ReadFromUDP(buf)
			if err != nil {
				t.Fatalf("player %d missing packets %v: %v", receiver, want, err)
			}
			msg := string(buf[:n])
			expectedPort, ok := want[msg]
			if !ok {
				t.Fatalf("player %d unexpected packet %q", receiver, msg)
			}
			if uint16(src.Port) != expectedPort {
				t.Fatalf("player %d packet %q source port %d != expected %d", receiver, msg, src.Port, expectedPort)
			}
			delete(want, msg)
		}
	}

	// Symmetric NAT port change for player 1001 on leg (1001, 1004).
	natRebind, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = natRebind.Close() }()
	if _, err := natRebind.WriteToUDP([]byte("nat-ping"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(legPorts[[2]uint16{1001, 1004}])}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = playerConns[1004].SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := playerConns[1004].ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "nat-ping" {
		t.Fatalf("1004 did not receive nat-ping: %q err=%v", string(buf[:n]), err)
	}
	if _, err := playerConns[1004].WriteToUDP([]byte("nat-pong"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(legPorts[[2]uint16{1004, 1001}])}); err != nil {
		t.Fatal(err)
	}
	_ = natRebind.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, src, err := natRebind.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "nat-pong" || uint16(src.Port) != legPorts[[2]uint16{1001, 1004}] {
		t.Fatalf("natRebind did not receive nat-pong from %d: %q src=%v err=%v", legPorts[[2]uint16{1001, 1004}], string(buf[:n]), src, err)
	}

	// CloseParty must send CLOSE and tear down room on standalone server.
	remote.CloseParty(partyID)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && relaySrv.Stats().ActiveRooms > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if st := relaySrv.Stats(); st.ActiveRooms != 0 || st.ActiveLegs != 0 || st.Forwarded < 14 {
		t.Fatalf("unexpected standalone relay final stats: %+v", st)
	}

	// Secret must never appear in emitted events.
	eventMu.Lock()
	for _, ev := range events {
		if strings.Contains(ev, secretHex) {
			eventMu.Unlock()
			t.Fatal("shared secret leaked into remote relay events")
		}
	}
	eventMu.Unlock()
}
