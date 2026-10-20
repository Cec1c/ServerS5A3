package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type r137ProbeTestConn struct {
	bufferConn
	remote net.TCPAddr
}

func (c *r137ProbeTestConn) RemoteAddr() net.Addr { return &c.remote }

func r137ProbeFixture(t *testing.T) (*inviteAcceptDungeonFixture, *gameSession) {
	t.Helper()
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.leader.conn = &r137ProbeTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40101}}
	fixture.follower.conn = &r137ProbeTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 40102}}

	repos, ok := fixture.service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "101", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "101", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	third := &gameSession{
		conn:            &r137ProbeTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.3"), Port: 40103}},
		connID:          "r132-third",
		channel:         fixture.channel,
		residentChannel: fixture.channel,
	}
	fixture.service.bindGameSessionCharacter(third, 101)
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 101, TownID: 38, AreaID: 1, Session: third})

	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.partyUDPRelay = relay
	t.Cleanup(func() { _ = relay.Close() })
	return fixture, third
}

func r137ProbeLegCount(relay *currentPartyUDPRelay) int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.legs)
}

// Loopback sockets model server-observed NAT endpoints, not a physical router.
// The existing in-memory fixture supplies only party/presence dependencies.
func r136Command(t *testing.T, s *Service, p *gameSession, op dnfenum.CmdPacket, body []byte) {
	t.Helper()
	handled, err := s.handleAlignedGameCommand(p, byte(dnfenum.GameCmdCommand), uint16(op), body)
	if err != nil || !handled {
		t.Fatalf("op%d handled=%v err=%v", op, handled, err)
	}
}

func r136Register(t *testing.T, s *Service, p *gameSession, port uint16, inner byte) {
	t.Helper()
	body := make([]byte, 31)
	body[0] = 5
	copy(body[1:5], []byte{192, 168, 50, inner})
	copy(body[5:9], []byte{203, 0, 113, 80}) // reported address never grants source authority
	binary.BigEndian.PutUint16(body[9:11], port)
	binary.LittleEndian.PutUint32(body[11:15], 1472)
	binary.LittleEndian.PutUint32(body[15:19], 12)
	copy(body[19:], []byte("LANFIXTURE00")) // identical synthetic machine token in both sessions
	frame, err := dnfproto.BuildChannelPacket(uint16(dnfenum.UpperMsgCharacterRoster), body, 1, dnfproto.DefaultChannelClassification)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.handleGameUpper(p, frame); err != nil {
		t.Fatal(err)
	}
	if got := currentPartyPeerPort(p); got != port {
		t.Fatalf("op2 port=%d want=%d", got, port)
	}
}

func r136Invite(t *testing.T, s *Service, a, b *gameSession) {
	t.Helper()
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, b.selectedCharacterID)
	r136Command(t, s, a, dnfenum.CmdPacketRequestPeer, body)
	notice := false
	for _, f := range splitUpperFrames(t, b.conn.(*r137ProbeTestConn).write.Bytes()) {
		if f.msgID == 7 {
			notice = true
		}
	}
	if !notice {
		t.Fatal("invite produced no op7 prompt")
	}
	r136Command(t, s, b, dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(a.selectedCharacterID, 0, 0))
	sa, sb := runtimePartyStateSnapshot(a), runtimePartyStateSnapshot(b)
	if sa.PartyID == 0 || sa.PartyID != sb.PartyID || len(runtimePartyMembers(sa)) < 2 {
		t.Fatalf("accept did not form shared party: A=%+v B=%+v", sa, sb)
	}
}

func r136Fixture(t *testing.T, ipB string, mapped bool, nativeProfile ...bool) (*inviteAcceptDungeonFixture, *gameSession, *net.UDPConn, *net.UDPConn) {
	t.Helper()
	f, third := r137ProbeFixture(t)
	if len(nativeProfile) > 0 && nativeProfile[0] {
		f.service.partyUDPRelay.Close()
		// Windows can allocate sequential ephemeral ports; repeatedly requesting
		// a port outside a 64-port future range is not a reliable fixture.
		probeSocket := testCurrentPartyUDPClient(t, "127.0.0.1")
		probe := probeSocket.LocalAddr().(*net.UDPAddr).Port
		start := probe + 1
		if start+63 > 65535 {
			start = probe - 64
		}
		probeSocket.Close()
		r, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
			enabled: true, advertiseIP: "127.0.0.1", portStart: start, portCount: 64,
			probeReplay:            readPartyUDPRelayProbeReplayMode("").enabled(),
			probeListen:            readPartyUDPRelayProbeListenMode("").enabled(),
			seedForward:            readPartyUDPRelaySeedForwardMode("").enabled(),
			observedEndpointFanout: readPartyUDPRelayObservedEndpointFanoutMode(""),
			probePorts:             []int{probe},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		if len(r.probeListenerPorts()) != 1 {
			t.Fatal("native-profile probe listener unavailable")
		}
		f.service.partyUDPRelay = r
	}
	f.service.options.currentClientVersion = "official-24.9"
	f.leader.conn.(*r137ProbeTestConn).remote.IP = net.ParseIP("127.0.0.1")
	f.follower.conn.(*r137ProbeTestConn).remote.IP = net.ParseIP(ipB)
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, ipB)
	t.Cleanup(func() { a.Close(); b.Close() })
	pa, pb := uint16(a.LocalAddr().(*net.UDPAddr).Port), uint16(b.LocalAddr().(*net.UDPAddr).Port)
	if mapped {
		pa, pb = 10000, 10000
	}
	r136Register(t, f.service, f.leader, pa, 10)
	r136Register(t, f.service, f.follower, pb, 11)
	ar, br := currentPartyPeerEndpointSnapshot(f.leader), currentPartyPeerEndpointSnapshot(f.follower)
	if !ar.PeerRowAccStamped || !br.PeerRowAccStamped || ar.PeerRowAcc == br.PeerRowAcc {
		t.Fatal("same token/IP merged session acc")
	}
	r136Invite(t, f.service, f.leader, f.follower)
	return f, third, a, b
}

// Decode the op11 actually emitted by the accept handler, not a fresh builder call.
func r136WireEndpoint(t *testing.T, s *Service, p *gameSession, peer uint16) *net.UDPAddr {
	t.Helper()
	var result *net.UDPAddr
	for _, f := range splitUpperFrames(t, p.conn.(*r137ProbeTestConn).write.Bytes()) {
		if f.msgID != 11 || len(f.body) < 1 || len(f.body) != 1+int(f.body[0])*22 {
			continue
		}
		for i := 0; i < int(f.body[0]); i++ {
			row := f.body[1+i*22 : 1+(i+1)*22]
			if binary.LittleEndian.Uint16(row[:2]) == peer {
				result = &net.UDPAddr{IP: net.IPv4(row[2], row[3], row[4], row[5]), Port: int(binary.BigEndian.Uint16(row[10:12]))}
			}
		}
	}
	if result == nil {
		t.Fatalf("no emitted op11 peer row for %d", peer)
	}
	state := runtimePartyStateSnapshot(p)
	ip, port, ok := s.partyUDPRelay.Endpoint(uint16(state.PartyID), p.selectedCharacterID, peer)
	if !ok || int(port) != result.Port || !result.IP.Equal(net.IP(ip[:])) {
		t.Fatal("wire endpoint differs from bound relay leg")
	}
	leg := r136Leg(t, s.partyUDPRelay, p.selectedCharacterID, peer)
	if leg.binding.generation != p.characterGeneration {
		t.Fatal("relay binding generation mismatch")
	}
	return result
}

func r136Leg(t *testing.T, r *currentPartyUDPRelay, owner, peer uint16) *currentPartyUDPRelayLeg {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *currentPartyUDPRelayLeg
	for k, l := range r.legs {
		if !k.pvp && k.ownerID == owner && k.peerID == peer {
			if found != nil {
				t.Fatal("duplicate directed owner across parties")
			}
			found = l
		}
	}
	if found == nil {
		t.Fatalf("missing directed leg %d->%d", owner, peer)
	}
	return found
}

func r136Write(t *testing.T, c *net.UDPConn, dst *net.UDPAddr, b []byte) {
	t.Helper()
	if _, err := c.WriteToUDP(b, dst); err != nil {
		t.Fatal(err)
	}
}

func r136Read(t *testing.T, c *net.UDPConn, want []byte, source *net.UDPAddr) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1024)
	for {
		n, src, err := c.ReadFromUDP(b)
		if err != nil {
			t.Fatalf("missing %q: %v", want, err)
		}
		if bytes.Equal(b[:n], want) {
			if source != nil && src.String() != source.String() {
				t.Fatalf("source=%s want=%s", src, source)
			}
			return
		}
	}
}

func r136Learn(t *testing.T, r *currentPartyUDPRelay, c *net.UDPConn, dst *net.UDPAddr, owner, peer uint16) {
	t.Helper()
	r136Write(t, c, dst, probePayload(owner))
	waitRelayCondition(t, 2*time.Second, "directed endpoint learned", func() bool {
		ep := r136Leg(t, r, owner, peer).learnedEndpointSnapshot()
		return ep != nil && ep.String() == c.LocalAddr().String()
	})
}

func r136Exchange(t *testing.T, f *inviteAcceptDungeonFixture, a, b *net.UDPConn) (*net.UDPAddr, *net.UDPAddr) {
	t.Helper()
	ab := r136WireEndpoint(t, f.service, f.leader, 100)
	ba := r136WireEndpoint(t, f.service, f.follower, 99)
	if ab.Port == ba.Port {
		t.Fatal("directed endpoints merged")
	}
	r136Learn(t, f.service.partyUDPRelay, b, ba, 100, 99)
	r136Learn(t, f.service.partyUDPRelay, a, ab, 99, 100)
	r136Read(t, b, probePayload(99), ba)
	r136Write(t, b, ba, probePayload(100))
	r136Read(t, a, probePayload(100), ab)
	for i := 0; i < 3; i++ {
		pa, pb := []byte(fmt.Sprintf("A-data-%d", i)), []byte(fmt.Sprintf("B-data-%d", i))
		r136Write(t, a, ab, pa)
		r136Read(t, b, pa, ba)
		r136Write(t, b, ba, pb)
		r136Read(t, a, pb, ab)
	}
	return ab, ba
}

func TestR136P70PartyAndBidirectionalUDP(t *testing.T) {
	for _, tc := range []struct {
		name, ip string
		mapped   bool
	}{
		{"same_observed_ip_distinct_ports", "127.0.0.1", false},
		{"same_ip_same_reported_port_nat_remapped", "127.0.0.1", true},
		{"different_observed_ip", "127.0.0.2", false},
	} {
		t.Run(tc.name, func(t *testing.T) { f, _, a, b := r136Fixture(t, tc.ip, tc.mapped); r136Exchange(t, f, a, b) })
	}
}

func TestR136P70ThirdMemberAndForeignIP(t *testing.T) {
	f, c, a, b := r136Fixture(t, "127.0.0.1", false)
	ab, ba := r136Exchange(t, f, a, b)
	wrong := testCurrentPartyUDPClient(t, "127.0.0.4")
	defer wrong.Close()
	leg := r136Leg(t, f.service.partyUDPRelay, 99, 100)
	r136Write(t, wrong, ab, probePayload(99))
	waitRelayCondition(t, time.Second, "foreign IP rejected", func() bool { return leg.relayLegStatsSnapshot().RejectedSource > 0 })
	if ep := leg.learnedEndpointSnapshot(); ep.String() != a.LocalAddr().String() {
		t.Fatal("foreign IP stole endpoint")
	}
	cUDP := testCurrentPartyUDPClient(t, "127.0.0.3")
	defer cUDP.Close()
	r136Register(t, f.service, c, uint16(cUDP.LocalAddr().(*net.UDPAddr).Port), 12)
	// Uninvited acceptance must not join or allocate legs.
	r136Command(t, f.service, c, dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(99, 0, 0))
	if r137ProbeLegCount(f.service.partyUDPRelay) != 2 {
		t.Fatal("unsolicited acceptance changed relay")
	}
	r136Invite(t, f.service, f.leader, c)
	if r137ProbeLegCount(f.service.partyUDPRelay) != 6 {
		t.Fatal("three-member party needs six directed legs")
	}
	ca := r136WireEndpoint(t, f.service, c, 99)
	ac := r136WireEndpoint(t, f.service, f.leader, 101)
	r136Learn(t, f.service.partyUDPRelay, cUDP, ca, 101, 99)
	r136Learn(t, f.service.partyUDPRelay, a, ac, 99, 101)
	r136Read(t, cUDP, probePayload(99), ca)
	r136Write(t, a, ab, []byte("AB-private"))
	r136Read(t, b, []byte("AB-private"), ba)
	if got := readClientDatagram(t, cUDP, 100*time.Millisecond); got != nil {
		t.Fatalf("third member received AB data: %q", got)
	}
	r136Write(t, cUDP, ca, []byte("CA-data"))
	r136Read(t, a, []byte("CA-data"), ac)
	r136Command(t, f.service, c, dnfenum.CmdPacketLeaveParty, nil)
	if r137ProbeLegCount(f.service.partyUDPRelay) != 2 {
		t.Fatal("third-member leave retained legs")
	}
	r136Write(t, a, ab, []byte("AB-after-leave"))
	r136Read(t, b, []byte("AB-after-leave"), ba)
}

func TestR136P70ReconnectThreeGenerations(t *testing.T) {
	f, _, a, b := r136Fixture(t, "127.0.0.1", false)
	_, priorBA := r136Exchange(t, f, a, b)
	partyID := runtimePartyStateSnapshot(f.leader).PartyID
	for i := 0; i < 3; i++ {
		old := f.follower
		replacement := &gameSession{conn: &r137ProbeTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41000 + i}}, connID: fmt.Sprintf("r136-reconnect-%d", i), channel: f.channel, residentChannel: f.channel, channelReconnect: true}
		f.service.bindGameSessionCharacter(replacement, 100)
		f.service.promoteResidentGameSession(replacement, 100)
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 100, TownID: 38, AreaID: 1, Session: replacement})
		nextUDP := testCurrentPartyUDPClient(t, "127.0.0.1")
		t.Cleanup(func() { nextUDP.Close() })
		r136Register(t, f.service, replacement, uint16(nextUDP.LocalAddr().(*net.UDPAddr).Port), 11)
		if replacement.characterGeneration <= old.characterGeneration {
			t.Fatal("generation did not advance")
		}
		state := runtimePartyStateSnapshot(replacement)
		if state.PartyID != partyID {
			t.Fatal("reconnect changed party identity")
		}
		// This is the production roster publication primitive after lifecycle promotion.
		if err := f.service.sendManagedRuntimePartySnapshots(state); err != nil {
			t.Fatal(err)
		}
		r136Command(t, f.service, old, dnfenum.CmdPacketLeaveParty, nil)
		current, ok := f.service.runtimePartyManagerForService().SnapshotByUser(100, replacement.characterGeneration)
		if !ok || len(current.Members) != 2 {
			t.Fatal("stale TCP leave removed replacement")
		}
		f.follower = replacement
		_, nextBA := r136Exchange(t, f, a, nextUDP)
		if priorBA.Port == nextBA.Port {
			t.Fatal("changed generation reused old directed socket")
		}
		r136Write(t, b, priorBA, []byte("retired-generation"))
		if got := readClientDatagram(t, a, 100*time.Millisecond); got != nil {
			t.Fatalf("retired UDP leg delivered %q", got)
		}
		if r137ProbeLegCount(f.service.partyUDPRelay) != 2 {
			t.Fatal("reconnect leaked legs")
		}
		priorBA, b = nextBA, nextUDP
	}
}

func TestR136P70NativeProbeBootstrap(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "127.0.0.2"} {
		t.Run(ip, func(t *testing.T) {
			f, _, a, b := r136Fixture(t, ip, true, true)
			r := f.service.partyUDPRelay
			probe := r.probeListenerPorts()[0]
			sendOneByteNativeProbe(t, a, probe, 1)
			sendOneByteNativeProbe(t, b, probe, 0)
			waitRelayCondition(t, time.Second, "two distinct observed endpoints", func() bool { return r.observedProbeEndpointCount() == 2 })
			ab := r136WireEndpoint(t, f.service, f.leader, 100)
			ba := r136WireEndpoint(t, f.service, f.follower, 99)
			r136Write(t, a, ab, probePayload(99))
			// B has not sent to its directed leg: bootstrap must use its observed endpoint.
			r136Read(t, b, probePayload(99), nil)
			r136Learn(t, r, b, ba, 100, 99)
			r136Read(t, a, probePayload(100), ab)
			r136Write(t, a, ab, []byte("native-profile-A"))
			r136Read(t, b, []byte("native-profile-A"), ba)
			r136Write(t, b, ba, []byte("native-profile-B"))
			r136Read(t, a, []byte("native-profile-B"), ab)
		})
	}
}

func TestR136P70SameIPLoginTokensRemainSeparate(t *testing.T) {
	f := newAccountResolutionTestService(t)
	sessions := []*gameSession{{connID: "r136-token-A", clientIP: "203.0.113.80"}, {connID: "r136-token-B", clientIP: "203.0.113.80"}}
	for i, p := range sessions {
		token, err := newLoginToken()
		if err != nil {
			t.Fatal(err)
		}
		account := fmt.Sprintf("r136-memory-account-%d", i)
		f.service.loginTokens.remember(token, account, time.Now().Add(time.Minute))
		if !f.service.bindGameSessionAccountFromLoginToken(p, dnfproto.DefaultChannelClassification, buildClientLoginBody(token)) {
			t.Fatal("synthetic token binding failed")
		}
		if got := f.service.accountIDForSession(p); got != account {
			t.Fatal("same-IP token account collapsed")
		}
	}
	if sessions[0].accountID == sessions[1].accountID || sessions[0].loginChannelFlowKey == sessions[1].loginChannelFlowKey {
		t.Fatal("distinct login tokens merged account/flow identity")
	}
	sessions[1].clientIP = "198.51.100.30"
	if got := f.service.accountIDForSession(sessions[1]); got != "r136-memory-account-1" {
		t.Fatal("VPN IP change changed authenticated account")
	}
}
