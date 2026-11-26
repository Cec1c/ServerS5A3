package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestCurrentMainWhisperAcceptsZeroGroupDirectoryFallback(t *testing.T) {
	s, source, target, src, dst := newCurrentSocialPeerTest(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.channelAdvertiseID = 1
	target.channel = channelcatalog.Channel{ID: 11}
	repos, _ := s.repositoryGroup()
	for _, p := range []*gameSession{source, target} {
		c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		c.Stats = map[string]int64{"server_group_id": 0}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}

	// Native 0x2A03916 falls back from target group 0 to advertised directory 1.
	body := mainWhisperRequest(0xffff, 1)
	if err := s.handleCurrentPeerChat(source, 17, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, src.write.Bytes())
	if ack.Header.MsgID != 17 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
		t.Fatalf("main whisper ACK differs: %+v rest=%d", ack, len(rest))
	}
	notice, rest := splitGameServerUpperPacket(t, dst.write.Bytes())
	var want packetWriter
	want.writeBytes([]byte{1, 0})
	want.writeRawDstr(rosterNameBytes("pouut"))
	want.writeByte(0)
	want.writeRawDstr([]byte("hello"))
	want.writeRawDstr(nil)
	if notice.Header.MsgID != 65 || notice.Header.Classification != 0 || !bytes.Equal(notice.Body, want.bytes()) || len(rest) != 0 {
		t.Fatalf("main whisper NOTI65 differs: hdr=%+v body=%x want=%x", notice.Header, notice.Body, want.bytes())
	}

	// Foreign trailing byte (2 != advertiseID 1) must still be rejected.
	src.write.Reset()
	dst.write.Reset()
	if err := s.handleCurrentPeerChat(source, 17, mainWhisperRequest(0xffff, 2)); err != nil {
		t.Fatal(err)
	}
	if dst.write.Len() != 0 {
		t.Fatal("foreign trailing group 2 must be rejected")
	}

	// Non-zero character group (7) must NOT accept directory fallback 1.
	for _, p := range []*gameSession{source, target} {
		c, _, _ := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		c.Stats = map[string]int64{"server_group_id": 7}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	src.write.Reset()
	dst.write.Reset()
	if err := s.handleCurrentPeerChat(source, 17, mainWhisperRequest(0xffff, 1)); err != nil {
		t.Fatal(err)
	}
	if dst.write.Len() != 0 {
		t.Fatal("non-zero group character must reject directory fallback 1")
	}
}

func TestCrossAreaSameChannelPartyInvitePrimesMode8BeforeNoti7AndAcceptFormsParty(t *testing.T) {
	f := newTownPartyActorHPFixture(t, false)
	s := f.service
	s.options.channelAdvertiseID = 1
	inviter, inviterConn := f.actor, f.actorConn
	invitee, inviteeConn := f.target, f.targetConn
	inviter.channel = channelcatalog.Channel{ID: 11}
	invitee.channel = channelcatalog.Channel{ID: 11}
	inviter.connectionTownActorOwnerChannel = 11
	invitee.connectionTownActorOwnerChannel = 11

	// Inviter in Hendon Myre (town 2, area 2); invitee in Seria's room (town 38, area 1, private instance).
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, ChannelID: 11, TownID: 2, AreaID: 2, Instance: 0, Session: inviter,
	})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, ChannelID: 11, TownID: 38, AreaID: 1, Instance: 29, Session: invitee,
	})
	if s.onlinePlayers.PeerInSameArea(17, 29) {
		t.Fatal("fixture precondition failed: players must be in different town areas")
	}
	f.reset()

	reqBody := make([]byte, 13)
	binary.LittleEndian.PutUint16(reqBody[0:2], 29)
	reqBody[2] = 0 // party invite mode 0
	if err := s.handleOnlineRequestPeer(inviter, uint16(dnfenum.CmdPacketRequestPeer), reqBody); err != nil {
		t.Fatalf("handleOnlineRequestPeer: %v", err)
	}

	// Inviter receives op10 ACK.
	inviterAck, rest := splitCurrentGameServerUpperPacketAuto(t, inviterConn.write.Bytes())
	if inviterAck.Header.MsgID != uint16(dnfenum.CmdPacketRequestPeer) || len(rest) != 0 {
		t.Fatalf("unexpected inviter packets: hdr=%+v rest=%d", inviterAck.Header, len(rest))
	}

	// Invitee receives mode8 priming frame BEFORE NOTI7.
	wire := inviteeConn.write.Bytes()
	if len(wire) == 0 {
		t.Fatal("expected mode8 + NOTI7 for cross-area invitee, got 0 bytes")
	}
	p1, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
	if p1.Header.Classification != 0 || p1.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || len(p1.Body) == 0 || p1.Body[0] != currentCrossSceneMode8Subtype {
		t.Fatalf("first invitee packet must be class0/op2 mode8 prime, got class=%d msg=%d body0=%v", p1.Header.Classification, p1.Header.MsgID, p1.Body[:min(1, len(p1.Body))])
	}
	p2, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if p2.Header.Classification != 0 || p2.Header.MsgID != 0x0007 || len(rest) != 0 {
		t.Fatalf("second invitee packet must be class0/op7 NOTI7, got class=%d msg=%d rest=%d", p2.Header.Classification, p2.Header.MsgID, len(rest))
	}

	// Invitee accepts via op11 -> party forms cleanly.
	inviterConn.write.Reset()
	inviteeConn.write.Reset()
	respBody := make([]byte, 7)
	binary.LittleEndian.PutUint16(respBody[0:2], 17)
	respBody[2] = 0
	if err := s.handleOnlineResponsePeer(invitee, uint16(dnfenum.CmdPacketResponsePeer), respBody); err != nil {
		t.Fatalf("handleOnlineResponsePeer: %v", err)
	}
	snap, found := s.runtimePartyManagerForService().SnapshotByUser(17, 0)
	if !found || len(snap.Members) != 2 {
		t.Fatalf("expected 2-member party after cross-area accept, found=%v snap=%+v", found, snap)
	}
}

func TestCrossAreaPartyInviteBlocksDifferentChannelAndActiveDungeon(t *testing.T) {
	f := newTownPartyActorHPFixture(t, false)
	s := f.service
	inviter, inviterConn := f.actor, f.actorConn
	invitee, inviteeConn := f.target, f.targetConn
	inviter.channel = channelcatalog.Channel{ID: 11}
	invitee.channel = channelcatalog.Channel{ID: 12}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, ChannelID: 11, TownID: 2, AreaID: 2, Session: inviter,
	})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, ChannelID: 12, TownID: 38, AreaID: 1, Instance: 29, Session: invitee,
	})
	f.reset()

	reqBody := make([]byte, 13)
	binary.LittleEndian.PutUint16(reqBody[0:2], 29)
	reqBody[2] = 0
	if err := s.handleOnlineRequestPeer(inviter, uint16(dnfenum.CmdPacketRequestPeer), reqBody); err != nil {
		t.Fatalf("cross-channel request peer: %v", err)
	}
	if inviteeConn.write.Len() != 0 {
		t.Fatal("cross-channel invite must not forward to invitee")
	}
	if inviterConn.write.Len() == 0 {
		t.Fatal("cross-channel invite should reject with failure packet")
	}

	// Same channel, but invitee is in active dungeon -> blocked.
	invitee.channel = channelcatalog.Channel{ID: 11}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, ChannelID: 11, TownID: 38, AreaID: 1, Instance: 29, Session: invitee,
	})
	invitee.dungeon.mu.Lock()
	invitee.dungeon.runtime = &runtimeDungeonState{Session: &worldmap.DungeonSession{}}
	invitee.dungeon.mu.Unlock()
	f.reset()
	if err := s.handleOnlineRequestPeer(inviter, uint16(dnfenum.CmdPacketRequestPeer), reqBody); err != nil {
		t.Fatalf("active-dungeon request peer: %v", err)
	}
	if inviteeConn.write.Len() != 0 || inviterConn.write.Len() != 0 {
		t.Fatalf("active-dungeon cross-area invite must be blocked, inviter=%d invitee=%d", inviterConn.write.Len(), inviteeConn.write.Len())
	}
}

func TestCrossAreaGuildRosterPrimesSameChannelPeerMode8AndDeduplicates(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	// Explicitly clear wedding reservation to test an ordinary non-wedding guild viewer.
	viewer.Stats["wedding_v1_phase"] = 0
	if err := dnfrepo.SaveCharacterFields(context.Background(), repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	f.target.channel.ID, f.target.connectionTownActorOwnerChannel = 11, 11
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("first non-wedding guild roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 1 {
		t.Fatalf("expected 1 silent mode8 prime for same-channel guild peer, got %d", got)
	}
	// Repeat request must deduplicate.
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("second non-wedding guild roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("expected 0 duplicate mode8 primes on repeat guild roster, got %d", got)
	}
	// Moving peer 17 to another channel must NOT trigger same-channel priming.
	actorSession := s.gameSessions[17]
	actorSession.channel.ID = 12
	actorSession.connectionTownActorOwnerChannel = 12
	f.targetConn.write.Reset()
	if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
		t.Fatalf("cross-channel guild roster failed: %v", err)
	}
	if got := countSilentMode8Frames(t, f.targetConn.write.Bytes()); got != 0 {
		t.Fatalf("expected 0 mode8 primes for cross-channel guild peer, got %d", got)
	}
}
