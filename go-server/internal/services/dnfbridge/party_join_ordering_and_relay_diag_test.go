package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestCrossAreaPartyJoinProjectsRemoteActorsBeforeEndpoints verifies that
// cross-area town joins create each member's remote CUser actor (mode0 + mode1
// + op23) BEFORE emitting op11 (PARTY_IP_INFO) and op9 (NOTI9), without sending
// a premature op9 before op11.
func TestCrossAreaPartyJoinProjectsRemoteActorsBeforeEndpoints(t *testing.T) {
	_, s, leader, joiner, leaderConn, joinerConn := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.partyFormationOrder = partyFormationOrderVendorWire
	s.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat

	// Place leader (1001) and joiner (1002) in different areas of the same town/channel.
	joinerInfo, ok := s.onlinePlayers.PlayerForCharacter(1002)
	if !ok {
		t.Fatal("joiner presence missing")
	}
	joinerInfo.AreaID++
	s.onlinePlayers.EnterArea(&joinerInfo)
	if s.onlinePlayers.PeerInSameArea(1001, 1002) {
		t.Fatal("expected leader and joiner in different town areas")
	}

	// Create leader's solo party so directory join attaches joiner to it.
	leaderIdentity, ok := s.boundGameSessionCharacterSnapshot(leader)
	if !ok {
		t.Fatal("leader identity missing")
	}
	created := s.runtimePartyManagerForService().Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: leaderIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("cross-area"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create solo party failed: %+v", created)
	}
	s.publishRuntimePartySnapshot(created.Party)
	leaderState := runtimePartyStateSnapshot(leader)
	leaderConn.write.Reset()
	joinerConn.write.Reset()

	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, uint16(leaderState.PartyID))
	handled, err := s.handleRuntimePartyDirectoryJoin(joiner, body)
	if !handled || err != nil {
		t.Fatalf("directory join handled=%t err=%v", handled, err)
	}

	for _, tc := range []struct {
		name string
		conn *bufferConn
	}{
		{name: "leader", conn: leaderConn},
		{name: "joiner", conn: joinerConn},
	} {
		frames := townDisplayFrames(t, tc.conn.write.Bytes())
		firstMode0, firstMode1, firstArea, firstOp11, firstOp9 := -1, -1, -1, -1, -1
		for i, f := range frames {
			switch f.msgID {
			case uint16(dnfenum.CmdPacketSetUDPIPPort):
				if len(f.body) > 0 && f.body[0] == 0 && firstMode0 == -1 {
					firstMode0 = i
				}
				if len(f.body) > 0 && f.body[0] == 1 && firstMode1 == -1 {
					firstMode1 = i
				}
			case currentTownUserAreaNotificationMsgID:
				if firstArea == -1 {
					firstArea = i
				}
			case 0x000b:
				if firstOp11 == -1 {
					firstOp11 = i
				}
			case uint16(dnfenum.CmdPacketRecoverStamina):
				if firstOp9 == -1 {
					firstOp9 = i
				}
			}
		}
		if firstMode0 == -1 || firstMode1 == -1 || firstArea == -1 {
			t.Fatalf("%s missing cross-area actor creation: mode0=%d mode1=%d area=%d", tc.name, firstMode0, firstMode1, firstArea)
		}
		if firstOp11 == -1 || firstOp9 == -1 {
			t.Fatalf("%s missing party snapshot frames: op11=%d op9=%d", tc.name, firstOp11, firstOp9)
		}
		if !(firstMode0 < firstMode1 && firstMode1 < firstArea && firstArea < firstOp11 && firstOp11 < firstOp9) {
			t.Fatalf("%s wrong wire ordering: mode0=%d mode1=%d area=%d op11=%d op9=%d (want mode0 < mode1 < area < op11 < op9)",
				tc.name, firstMode0, firstMode1, firstArea, firstOp11, firstOp9)
		}
	}
}

type orderedWriteConn struct {
	bufferConn
	label string
	mu    *sync.Mutex
	order *[]string
}

func (c *orderedWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	*c.order = append(*c.order, c.label)
	c.mu.Unlock()
	return c.bufferConn.Write(p)
}

func (c *orderedWriteConn) LocalAddr() net.Addr                { return c.bufferConn.LocalAddr() }
func (c *orderedWriteConn) RemoteAddr() net.Addr               { return c.bufferConn.RemoteAddr() }
func (c *orderedWriteConn) SetDeadline(t time.Time) error      { return nil }
func (c *orderedWriteConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *orderedWriteConn) SetWriteDeadline(t time.Time) error { return nil }

type rejectWritesConn struct {
	bufferConn
}

func (c *rejectWritesConn) Write([]byte) (int, error) { return 0, net.ErrClosed }

func firstTownFrameIndex(frames []upperFrame, match func(upperFrame) bool) int {
	for index, frame := range frames {
		if match(frame) {
			return index
		}
	}
	return -1
}

// TestManagedPartySnapshotsFanOutAllMembersBeforeBystanders verifies that
// sendManagedRuntimePartySnapshots delivers op11/op9/op153 to ALL party members
// before running broadcastCurrentPartyActorProjection to unrelated same-area
// town bystanders.
func TestManagedPartySnapshotsFanOutAllMembersBeforeBystanders(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	_, s, leader, member, _, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.partyFormationOrder = partyFormationOrderVendorWire

	var mu sync.Mutex
	var writeOrder []string
	leaderWrap := &orderedWriteConn{label: "leader", mu: &mu, order: &writeOrder}
	memberWrap := &orderedWriteConn{label: "member", mu: &mu, order: &writeOrder}
	bystanderWrap := &orderedWriteConn{label: "bystander", mu: &mu, order: &writeOrder}
	leader.conn = leaderWrap
	member.conn = memberWrap

	// Add a same-area town bystander (1003) that is NOT in the party.
	bystander := &gameSession{
		conn:                            bystanderWrap,
		channel:                         leader.channel,
		selectedCharacterID:             1003,
		townActorOwnerChannel:           leader.townActorOwnerChannel,
		connectionTownActorOwnerChannel: leader.connectionTownActorOwnerChannel,
	}
	s.bindGameSessionCharacter(bystander, 1003)
	leaderInfo, _ := s.onlinePlayers.PlayerForCharacter(1001)
	bystanderInfo := leaderInfo
	bystanderInfo.CharacterID = 1003
	bystanderInfo.AccountID = "dnf:1003"
	bystanderInfo.Name = "bystander"
	bystanderInfo.Session = bystander
	s.onlinePlayers.EnterArea(&bystanderInfo)

	// Mark leader as display-only on bystander to trigger upgrade work during bystander broadcast.
	leaderIdentity, _ := s.boundGameSessionCharacterSnapshot(leader)
	bystander.rememberTownDisplayPeer(leaderIdentity)

	state, _, ok := s.createOrJoinManagedRuntimeParty(member, leader)
	if !ok {
		t.Fatal("failed to create managed party")
	}
	mu.Lock()
	writeOrder = nil
	mu.Unlock()

	if err := s.sendManagedRuntimePartySnapshots(state); err != nil {
		t.Fatalf("sendManagedRuntimePartySnapshots: %v", err)
	}

	mu.Lock()
	snapshotOrder := append([]string(nil), writeOrder...)
	mu.Unlock()

	lastMemberWrite := -1
	firstBystanderWrite := -1
	for i, label := range snapshotOrder {
		if label == "leader" || label == "member" {
			lastMemberWrite = i
		}
		if label == "bystander" && firstBystanderWrite == -1 {
			firstBystanderWrite = i
		}
	}
	if lastMemberWrite == -1 {
		t.Fatalf("no party member writes recorded: %v", snapshotOrder)
	}
	if firstBystanderWrite == -1 {
		t.Fatalf("expected bystander projection writes after party members, got: %v", snapshotOrder)
	}
	if firstBystanderWrite < lastMemberWrite {
		t.Fatalf("bystander writes (%d) interleaved before last party member write (%d): %v",
			firstBystanderWrite, lastMemberWrite, snapshotOrder)
	}
}

// TestManagedPartySnapshotsPrimeAndFanOutSameAuthoritativeRevision models a
// concurrent join landing after the initiating command received its state but
// before snapshot publication. The stale caller state contains two members;
// actor priming and the op11/op9 fan-out must both use the three-member manager
// snapshot read by the publication itself.
func TestManagedPartySnapshotsPrimeAndFanOutSameAuthoritativeRevision(t *testing.T) {
	repositories, s, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.partyFormationOrder = partyFormationOrderVendorWire
	s.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat

	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:3", State: "active"}); err != nil {
		t.Fatalf("save third account: %v", err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "1003", AccountID: "dnf:3", Name: "third", Job: "2", Level: 90,
	}); err != nil {
		t.Fatalf("save third character: %v", err)
	}
	thirdConn := &bufferConn{}
	third := &gameSession{
		conn: thirdConn, channel: leader.channel, accountID: "dnf:3", selectedCharacterID: 1003,
		townActorOwnerChannel: 42, connectionTownActorOwnerChannel: 42,
	}
	s.bindGameSessionCharacter(third, 1003)
	third.townMu.Lock()
	third.townSceneReadyCharacterID = third.selectedCharacterID
	third.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: third.selectedCharacterID, PositionValid: true, PositionX: 460, PositionY: 246,
	}
	third.townMu.Unlock()
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1003, AccountID: "dnf:3", Name: "third", TownID: 38, AreaID: 3,
		PositionX: 460, PositionY: 246, Direction: 5, AreaState: 3, Session: third,
	})

	staleState, _, joined := s.createOrJoinManagedRuntimeParty(member, leader)
	if !joined || len(runtimePartyMembers(staleState)) != 2 {
		t.Fatalf("initial party state=%+v joined=%t, want two members", staleState, joined)
	}
	thirdIdentity, ok := s.boundGameSessionCharacterSnapshot(third)
	if !ok {
		t.Fatal("third identity was not bound")
	}
	thirdMember, ok := s.runtimePartyMemberForIdentity(thirdIdentity)
	if !ok {
		t.Fatal("third runtime member could not be built")
	}
	concurrent := s.runtimePartyManagerForService().Join(uint16(staleState.PartyID), thirdMember)
	if !concurrent.OK || len(concurrent.Party.Members) != 3 {
		t.Fatalf("concurrent join result=%+v, want three-member snapshot", concurrent)
	}

	for _, conn := range []*bufferConn{leaderConn, memberConn, thirdConn} {
		conn.write.Reset()
	}
	if err := s.sendManagedRuntimePartySnapshotsForInitiator(leader, staleState); err != nil {
		t.Fatalf("sendManagedRuntimePartySnapshotsForInitiator: %v", err)
	}

	leaderFrames := townDisplayFrames(t, leaderConn.write.Bytes())
	firstMode0 := firstTownFrameIndex(leaderFrames, func(frame upperFrame) bool {
		return frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(frame.body) > 0 && frame.body[0] == 0
	})
	firstMode1 := firstTownFrameIndex(leaderFrames, func(frame upperFrame) bool {
		return frame.msgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(frame.body) > 0 && frame.body[0] == 1
	})
	firstArea := firstTownFrameIndex(leaderFrames, func(frame upperFrame) bool {
		return frame.msgID == currentTownUserAreaNotificationMsgID
	})
	firstOp11 := firstTownFrameIndex(leaderFrames, func(frame upperFrame) bool { return frame.msgID == 0x000b })
	firstOp9 := firstTownFrameIndex(leaderFrames, func(frame upperFrame) bool {
		return frame.msgID == uint16(dnfenum.CmdPacketRecoverStamina)
	})
	if firstMode0 == -1 || firstMode1 == -1 || firstArea == -1 || firstOp11 == -1 || firstOp9 == -1 {
		t.Fatalf("leader missing actor or roster frames: mode0=%d mode1=%d area=%d op11=%d op9=%d",
			firstMode0, firstMode1, firstArea, firstOp11, firstOp9)
	}
	if !(firstMode0 < firstMode1 && firstMode1 < firstArea && firstArea < firstOp11 && firstOp11 < firstOp9) {
		t.Fatalf("fresh member was not primed before roster: mode0=%d mode1=%d area=%d op11=%d op9=%d",
			firstMode0, firstMode1, firstArea, firstOp11, firstOp9)
	}
	thirdFrames := townDisplayFrames(t, thirdConn.write.Bytes())
	if firstTownFrameIndex(thirdFrames, func(frame upperFrame) bool { return frame.msgID == 0x000b }) == -1 ||
		firstTownFrameIndex(thirdFrames, func(frame upperFrame) bool {
			return frame.msgID == uint16(dnfenum.CmdPacketRecoverStamina)
		}) == -1 {
		t.Fatalf("fresh third member did not receive the roster fan-out: %+v", thirdFrames)
	}
}

// TestManagedPartySnapshotsProjectOnlyLocallyDeliveredMembers reproduces a
// roster write failure for one recipient. Its actor must not be projected to
// the same-area bystander, while the healthy member's actor still is.
func TestManagedPartySnapshotsProjectOnlyLocallyDeliveredMembers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fail      string
		healthy   uint16
		wantError bool
	}{
		{name: "peer failure", fail: "member", healthy: 1001},
		{name: "initiator failure", fail: "leader", healthy: 1002, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
			s.options.currentClientVersion = "official-24.9"
			s.options.partyFormationOrder = partyFormationOrderVendorWire

			bystanderConn := &bufferConn{}
			bystander := &gameSession{
				conn: bystanderConn, channel: leader.channel, selectedCharacterID: 1003,
				townActorOwnerChannel: 42, connectionTownActorOwnerChannel: 42,
			}
			s.bindGameSessionCharacter(bystander, 1003)
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{
				CharacterID: 1003, AccountID: "dnf:3", Name: "bystander", TownID: 38, AreaID: 2,
				PositionX: 454, PositionY: 238, Direction: 5, AreaState: 3, Session: bystander,
			})

			state, _, joined := s.createOrJoinManagedRuntimeParty(member, leader)
			if !joined {
				t.Fatal("failed to create managed party")
			}
			for _, conn := range []*bufferConn{leaderConn, memberConn, bystanderConn} {
				conn.write.Reset()
			}
			var initiator *gameSession
			switch tc.fail {
			case "leader":
				leader.conn = &rejectWritesConn{}
				initiator = leader
			case "member":
				member.conn = &rejectWritesConn{}
			default:
				t.Fatalf("unknown failure target %q", tc.fail)
			}

			err := s.sendManagedRuntimePartySnapshotsForInitiator(initiator, state)
			if tc.wantError {
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("initiator failure error=%v, want net.ErrClosed", err)
				}
			} else if err != nil {
				t.Fatalf("peer failure leaked into return value: %v", err)
			}

			projected := make(map[uint16]bool)
			for _, frame := range townDisplayFrames(t, bystanderConn.write.Bytes()) {
				if frame.msgID != uint16(dnfenum.CmdPacketRecoverStamina) || len(frame.body) < 10 ||
					frame.body[9] != currentSceneOp9ActorDisplayKind {
					continue
				}
				projected[binary.LittleEndian.Uint16(frame.body[4:6])] = true
			}
			if !projected[tc.healthy] {
				t.Fatalf("healthy member %d actor was not projected to bystander: %+v", tc.healthy, projected)
			}
			failedID := uint16(1001)
			if tc.fail == "member" {
				failedID = 1002
			}
			if projected[failedID] {
				t.Fatalf("failed local recipient %d was still projected to bystander: %+v", failedID, projected)
			}
		})
	}
}

// TestPartyUDPRelayReportTrafficTickOnlyWarnsOnDelta verifies that
// reportTrafficTick suppresses repetitive WARN spam once a leg stops dropping
// packets, warns again only if new drops accumulate, and prunes closed legs.
func TestPartyUDPRelayReportTrafficTickOnlyWarnsOnDelta(t *testing.T) {
	var mu sync.Mutex
	var logs []map[string]any
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", 31500, 16, func(msg string, args ...any) {
		entry := map[string]any{"msg": msg}
		for i := 0; i+1 < len(args); i += 2 {
			if key, ok := args[i].(string); ok {
				entry[key] = args[i+1]
			}
		}
		mu.Lock()
		logs = append(logs, entry)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("newCurrentPartyUDPRelay: %v", err)
	}
	defer func() { _ = relay.Close() }()

	if !relay.Sync(7, []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 102, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("relay.Sync failed")
	}

	relay.mu.Lock()
	leg := relay.legs[currentPartyUDPRelayLegKey{partyID: 7, ownerID: 101, peerID: 102}]
	opp := relay.legs[currentPartyUDPRelayLegKey{partyID: 7, ownerID: 102, peerID: 101}]
	relay.mu.Unlock()
	if leg == nil || opp == nil {
		t.Fatal("expected relay legs")
	}

	last := make(map[*currentPartyUDPRelayLeg]relayTrafficCounts)

	// Clear leg listening logs before testing traffic summaries.
	mu.Lock()
	logs = nil
	mu.Unlock()

	// Initial 3 unlearned handshake probes: tick 1 must emit one summary with delta_unlearned=3.
	leg.traffic.unlearned.Add(3)
	relay.reportTrafficTick(last)

	mu.Lock()
	if len(logs) != 1 {
		t.Fatalf("tick 1 logs = %d, want 1: %+v", len(logs), logs)
	}
	if logs[0]["unlearned"] != uint64(3) || logs[0]["delta_unlearned"] != uint64(3) || logs[0]["peer_learned"] != false {
		t.Fatalf("tick 1 fields unexpected: %+v", logs[0])
	}
	logs = nil
	mu.Unlock()

	// Peer learns endpoint; no more drops occur. Tick 2 must emit ZERO logs.
	opp.learnedPort.Store(40001)
	relay.reportTrafficTick(last)
	mu.Lock()
	if len(logs) != 0 {
		t.Fatalf("tick 2 expected 0 logs when no new drops, got %d: %+v", len(logs), logs)
	}
	mu.Unlock()

	// New drops occur later: tick 3 emits delta=7, total=10.
	leg.traffic.unlearned.Add(7)
	relay.reportTrafficTick(last)
	mu.Lock()
	if len(logs) != 1 {
		t.Fatalf("tick 3 logs = %d, want 1: %+v", len(logs), logs)
	}
	if logs[0]["unlearned"] != uint64(10) || logs[0]["delta_unlearned"] != uint64(7) || logs[0]["peer_learned"] != true {
		t.Fatalf("tick 3 fields unexpected: %+v", logs[0])
	}
	mu.Unlock()

	// Closing the party and running another tick prunes closed legs from last.
	relay.CloseParty(7)
	relay.reportTrafficTick(last)
	if len(last) != 0 {
		t.Fatalf("expected closed legs pruned from last map, got %d", len(last))
	}

	// Silence unused import guard if alignedcmd changes.
	_ = alignedcmd.PartyState{}
}
