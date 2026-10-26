package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	r195PreformationSourceArea = 9
	r195PreformationTargetArea = 14
	r195PreformationWait       = 5 * time.Second
)

type r195PreformationMoveResult struct {
	charID uint16
	err    error
}

type r195PreformationActorArea struct {
	town byte
	area byte
}

type r195PreformationReceiverWire struct {
	charID     uint16
	packets    []dnfproto.ChannelPacket
	firstOP11  int
	actorAreas map[uint16]r195PreformationActorArea
}

// The real area-9 -> area-14 op36 path removes the moving actor from the
// stationary client's town scene through class0/op6. The first temporary-party
// formation must restore that remote actor before its op11 arrives.
func TestR195FiendWarPreformationActorsLeaderOnly(t *testing.T) {
	f, peers := r195PreformationFixture(t, false)
	wires := r195PreformationMoveAndCapture(t, f, peers, 1)
	removed := false
	for _, packet := range wires[1].packets[:wires[1].firstOP11] {
		removed = removed || (packet.Header.Classification == 0 && packet.Header.MsgID == currentTownUserLeaveMsgID &&
			len(packet.Body) == 2 && binary.LittleEndian.Uint16(packet.Body) == peers[0].selectedCharacterID)
	}
	if !removed {
		t.Fatal("real cross-area move did not remove the leader from the stationary receiver before formation")
	}
	r195AssertPreformationActors(t, peers, wires)
}

// Both members commit the real op36 move before automatic admission runs.
func TestR195FiendWarPreformationActorsBothMove(t *testing.T) {
	f, peers := r195PreformationFixture(t, false)
	wires := r195PreformationMoveAndCapture(t, f, peers, len(peers))
	r195AssertPreformationActors(t, peers, wires)
}

// A one-member formation owns no remote actor. Its endpoint packet must still
// exist, with no fabricated peer mode0/mode1 bootstrap.
func TestR195FiendWarPreformationActorsSoloControl(t *testing.T) {
	f, peers := r195PreformationFixture(t, true)
	wires := r195PreformationMoveAndCapture(t, f, peers, 1)
	r195AssertPreformationActors(t, peers, wires)
}

func r195PreformationFixture(t *testing.T, solo bool) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	f, peers := r134N1Fixture(t)
	if len(peers) != 2 {
		t.Fatalf("fixture peers=%d want 2", len(peers))
	}
	s, b := f.service, f.state.Battle
	source, target := r195PreformationSourceArea, r195PreformationTargetArea
	from, sourceOK := b.FiendWar.Config.Areas[source]
	to, targetOK := b.FiendWar.Config.Areas[target]
	if !sourceOK || !targetOK || source == target ||
		from.Dungeon != 4957 || from.WorldMap != 119 ||
		to.Dungeon != 4962 || to.WorldMap != 124 || to.MemberMax < 2 ||
		!r195PreformationContainsArea(from.Next, target) || !r195PreformationContainsArea(to.Next, source) {
		t.Fatalf("real PVF 9/14 route changed: from=%+v to=%+v", from, to)
	}
	if !b.FiendWar.AreaOpen(target) {
		t.Fatalf("real target area %d is not open at initial battle state: %+v", target, b.FiendWar.Areas[target])
	}
	if solo {
		removed := peers[1].selectedCharacterID
		removedGroup := b.MemberGroups[removed]
		f.state.Members = f.state.Members[:1]
		delete(b.MemberAccounts, removed)
		delete(b.MemberGroups, removed)
		delete(b.FiendWar.MemberAreas, removed)
		delete(b.FiendWar.GroupAreas, removedGroup)
		s.onlinePlayers.LeaveArea(removed)
		peers = peers[:1]
	}

	targetArea, found := s.townArea(int64(b.FiendWar.Config.WaitingTown), int64(target))
	if !found {
		t.Fatalf("real town area %d is missing", target)
	}
	if err := s.preloadSkillCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers {
		if peer.channel.ID <= 0 || peer.channel.ID > 255 {
			t.Fatalf("fixture channel %d is not a representable owner byte", peer.channel.ID)
		}
		// This is the same owner pair committed by normal login/bootstrap. The
		// r134N1 fixture builds sessions directly, so it must initialize it here
		// before the candidate's fail-closed formation-actor preflight.
		owner := byte(peer.channel.ID)
		peer.connectionTownActorOwnerChannel = owner
		peer.townActorOwnerChannel = owner
		if got := currentTownActorOwnerContext(peer); got != owner {
			t.Fatalf("fixture char%d owner=%d want committed channel %d", peer.selectedCharacterID, got, owner)
		}
		r134N1Presence(t, f, peer, source)
		character, found, err := f.repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil || !found {
			t.Fatalf("load persisted character char%d found=%t err=%v", peer.selectedCharacterID, found, err)
		}
		character.Stats["area_state"] = 0
		if err := f.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatalf("persist area_state char%d: %v", peer.selectedCharacterID, err)
		}
		if _, err := s.ensureCharacterInitializationSnapshot(context.Background(), f.repos, character); err != nil {
			t.Fatalf("prepare logged-in character %d: %v", peer.selectedCharacterID, err)
		}
		quests, _, err := f.repos.Quest.Load(context.Background(), character.CharacterID)
		if err != nil {
			t.Fatalf("load quests char%d: %v", peer.selectedCharacterID, err)
		}
		if quests.States == nil {
			quests.States = make(map[int64]dnfrepo.QuestState, len(targetArea.NeedQuests))
		}
		quests.CharacterID = character.CharacterID
		for _, questID := range targetArea.NeedQuests {
			quests.States[questID] = dnfrepo.QuestState{Status: "completed"}
		}
		if err := f.repos.Quest.Save(context.Background(), quests); err != nil {
			t.Fatalf("persist target quests char%d: %v", peer.selectedCharacterID, err)
		}
		if state := s.runtimePartyStateForSession(peer); state.PartyID != 0 {
			t.Fatalf("fixture char%d starts in party %d", peer.selectedCharacterID, state.PartyID)
		}
	}
	for _, peer := range peers {
		if err := s.startGameSessionEvents(context.Background(), peer); err != nil {
			t.Fatalf("start session events char%d: %v", peer.selectedCharacterID, err)
		}
		peer := peer
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			_ = s.stopGameSessionEvents(ctx, peer)
		})
		r134N1Reset(peer)
	}
	return f, peers
}

func r195PreformationContainsArea(areas []int, want int) bool {
	for _, area := range areas {
		if area == want {
			return true
		}
	}
	return false
}

// r195PreformationMoveAndCapture holds every real op36 callback until all
// requests have committed their position and pending move. It then releases
// only the first peer (the leader), waits for the formation on every receiver,
// and snapshots those wires before releasing the remaining callbacks.
func r195PreformationMoveAndCapture(
	t *testing.T,
	f *raidStartGuardFixture,
	peers []*gameSession,
	moving int,
) []r195PreformationReceiverWire {
	t.Helper()
	if moving < 1 || moving > len(peers) {
		t.Fatalf("moving=%d peers=%d", moving, len(peers))
	}
	s := f.service
	gates := make([]*r195SessionGate, moving)
	arrived := make(chan r195PreformationMoveResult, moving)
	defer func() {
		for _, gate := range gates {
			gate.open()
		}
	}()
	for i, peer := range peers[:moving] {
		gate := &r195SessionGate{release: make(chan struct{}), done: make(chan error, 1)}
		gates[i] = gate
		go func(peer *gameSession, gate *r195SessionGate) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			gate.done <- s.callGameSession(ctx, peer, "r195-preformation-real-op36", func() error {
				peer.townMu.Lock()
				x, y := peer.townPositionSnapshot.PositionX, peer.townPositionSnapshot.PositionY
				peer.townMu.Unlock()
				err := s.handleTownSetUserArea(peer, buildTownMoveRequest(
					98, byte(r195PreformationTargetArea), int16(x), int16(y), 5))
				if err == nil {
					peer.townMu.Lock()
					pending := peer.fiendWarPendingMove
					if pending == nil || pending.from != r195PreformationSourceArea || pending.to != r195PreformationTargetArea {
						err = fmt.Errorf("op36 pending char%d=%+v want %d->%d",
							peer.selectedCharacterID, pending, r195PreformationSourceArea, r195PreformationTargetArea)
					}
					peer.townMu.Unlock()
				}
				arrived <- r195PreformationMoveResult{charID: peer.selectedCharacterID, err: err}
				select {
				case <-gate.release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return err
			})
		}(peer, gate)
	}
	seen := make(map[uint16]bool, moving)
	deadline := time.Now().Add(20 * time.Second)
	for len(seen) < moving {
		select {
		case result := <-arrived:
			if result.err != nil {
				t.Fatalf("real op36 char%d: %v\n%s", result.charID, result.err, r195RelevantLogs(f.logText(t)))
			}
			seen[result.charID] = true
		case <-time.After(time.Until(deadline)):
			t.Fatalf("real op36 did not finish moving=%d seen=%v\n%s", moving, seen, f.logText(t))
		}
	}
	// Capture authoritative town positions before entry removes online presence.
	areas := make(map[uint16]r195PreformationActorArea, len(peers))
	for _, peer := range peers {
		info, found := s.onlinePlayers.PlayerForCharacter(peer.selectedCharacterID)
		if !found {
			t.Fatal("preformation town presence missing")
		}
		areas[peer.selectedCharacterID] = r195PreformationActorArea{town: info.TownID, area: info.AreaID}
	}
	gates[0].open()
	wires := r195PreformationWaitFormation(t, f, peers)
	for i := range wires {
		wires[i].actorAreas = areas
	}
	for _, gate := range gates[1:] {
		gate.open()
	}
	for _, gate := range gates {
		select {
		case err := <-gate.done:
			if err != nil {
				t.Fatalf("op36 mailbox: %v", err)
			}
		case <-time.After(createWriteTimeout):
			t.Fatalf("op36 mailbox did not finish after formation")
		}
	}
	return wires
}

func r195PreformationWaitFormation(
	t *testing.T,
	f *raidStartGuardFixture,
	peers []*gameSession,
) []r195PreformationReceiverWire {
	t.Helper()
	deadline := time.Now().Add(r195PreformationWait)
	for {
		wires, ready := r195PreformationSnapshot(t, f, peers)
		if ready {
			return wires
		}
		if time.Now().After(deadline) {
			var ids []uint16
			for _, wire := range wires {
				ids = append(ids, wire.charID)
			}
			t.Fatalf("formation op11 missing on char(s)=%v packets=%s\n%s",
				ids, r195PreformationWireSummary(wires), r195RelevantLogs(f.logText(t)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func r195PreformationSnapshot(
	t *testing.T,
	f *raidStartGuardFixture,
	peers []*gameSession,
) ([]r195PreformationReceiverWire, bool) {
	t.Helper()
	wires := make([]r195PreformationReceiverWire, 0, len(peers))
	ready := true
	for _, peer := range peers {
		conn := peer.conn.(*partyDungeonSyncConn)
		var packets []dnfproto.ChannelPacket
		stream := conn.frames()
		for len(stream) != 0 {
			var packet dnfproto.ChannelPacket
			var rest []byte
			// Town op6 uses fixed16 too; the shared auto-parser covers op23
			// and other scene packets but does not include this opcode.
			if len(stream) >= 3 && stream[0] == 0 && binary.LittleEndian.Uint16(stream[1:3]) == currentTownUserLeaveMsgID {
				packet, rest = splitLongHengGameServerUpperPacket(t, stream)
			} else {
				packet, rest = splitCurrentGameServerUpperPacketAuto(t, stream)
			}
			packets = append(packets, packet)
			stream = rest
		}
		first := -1
		for i, packet := range packets {
			if packet.Header.Classification == 0 && packet.Header.MsgID == 0x000b {
				first = i
				break
			}
		}
		if first < 0 {
			ready = false
		}
		areas := make(map[uint16]r195PreformationActorArea, len(peers))
		for _, member := range peers {
			if info, found := f.service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID); found {
				areas[member.selectedCharacterID] = r195PreformationActorArea{town: info.TownID, area: info.AreaID}
			}
		}
		wires = append(wires, r195PreformationReceiverWire{
			charID: peer.selectedCharacterID, packets: packets, firstOP11: first, actorAreas: areas,
		})
	}
	return wires, ready
}

func r195AssertPreformationActors(
	t *testing.T,
	peers []*gameSession,
	wires []r195PreformationReceiverWire,
) {
	t.Helper()
	if len(peers) != len(wires) {
		t.Fatalf("receivers=%d wires=%d", len(peers), len(wires))
	}
	wantPeerCount := len(peers) - 1
	// Check the stationary follower first: that is the side that actually
	// received the leader's op6 in the reported crossing sequence.
	for i := len(peers) - 1; i >= 0; i-- {
		receiver := peers[i]
		wire := wires[i]
		if wire.firstOP11 < 0 {
			t.Fatalf("char%d has no formation op11: %s", receiver.selectedCharacterID, r195PreformationWireSummary(wires))
		}
		endpoint := wire.packets[wire.firstOP11]
		if len(endpoint.Body) == 0 || int(endpoint.Body[0]) != wantPeerCount {
			t.Fatalf("char%d first op11 body=%x want peer_count=%d", receiver.selectedCharacterID, endpoint.Body, wantPeerCount)
		}
		if receiver.channel.ID <= 0 || receiver.channel.ID > 255 {
			t.Fatalf("char%d receiver channel=%d is not a real owner byte", receiver.selectedCharacterID, receiver.channel.ID)
		}
		wantOwner := byte(receiver.channel.ID)
		// Inspect the captured wire owner. The live session may already be in
		// dungeon context (owner 0) by the time this assertion executes.
		for index := 0; index < wire.firstOP11; index++ {
			packet := wire.packets[index]
			if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketRecoverStamina) &&
				(len(packet.Body) <= 9 || packet.Body[9] != currentSceneOp9ActorRemoveKind) {
				t.Fatalf("char%d received op9 before first op11 at frame %d: body=%x",
					receiver.selectedCharacterID, index, packet.Body)
			}
		}
		for _, actor := range peers {
			if actor == receiver {
				continue
			}
			r195AssertPreformationRemoteActor(t, wire, receiver, actor, wantOwner, peers)
		}
	}
}

func r195AssertPreformationRemoteActor(
	t *testing.T,
	wire r195PreformationReceiverWire,
	receiver, actor *gameSession,
	wantOwner byte,
	all []*gameSession,
) {
	t.Helper()
	lastRemove, mode0, mode1 := -1, -1, -1
	for index, packet := range wire.packets[:wire.firstOP11] {
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case currentTownUserLeaveMsgID:
			if len(packet.Body) >= 2 && binary.LittleEndian.Uint16(packet.Body[:2]) == actor.selectedCharacterID {
				lastRemove = index
				mode0, mode1 = -1, -1
			}
		case uint16(dnfenum.CmdPacketSetUDPIPPort):
			if len(packet.Body) == 0 {
				continue
			}
			switch packet.Body[0] {
			case 0:
				if len(packet.Body) < 0x4e || packet.Body[3] != currentSceneObjectRoute ||
					packet.Body[4] != wantOwner ||
					binary.LittleEndian.Uint16(packet.Body[0x4c:0x4e]) != actor.selectedCharacterID {
					continue
				}
				if mode0 < 0 {
					mode0 = index
				}
			case 1:
				if len(packet.Body) < currentMode1BaseWireSize || packet.Body[3] != currentSceneObjectRoute ||
					packet.Body[4] != wantOwner ||
					binary.LittleEndian.Uint16(packet.Body[0x15:0x17]) != actor.selectedCharacterID {
					continue
				}
				if mode1 < 0 {
					mode1 = index
				}
			}
		}
	}
	if mode0 < 0 || mode1 < 0 || mode0 >= mode1 {
		t.Fatalf("char%d lacks complete remote mode0+mode1 for char%d before first op11: %s",
			receiver.selectedCharacterID, actor.selectedCharacterID, r195PreformationWireSummary([]r195PreformationReceiverWire{wire}))
	}
	if lastRemove >= 0 && (mode0 <= lastRemove || mode1 <= lastRemove) {
		t.Fatalf("char%d remote char%d bootstrap mode0=%d mode1=%d is not after last op6 remove=%d",
			receiver.selectedCharacterID, actor.selectedCharacterID, mode0, mode1, lastRemove)
	}
	areaIndex := -1
	for index := mode1 + 1; index < wire.firstOP11; index++ {
		packet := wire.packets[index]
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentTownUserAreaNotificationMsgID &&
			len(packet.Body) >= 4 && binary.LittleEndian.Uint16(packet.Body[:2]) == actor.selectedCharacterID {
			areaIndex = index
			break
		}
	}
	if areaIndex < 0 {
		t.Fatalf("char%d remote char%d mode1 at %d has no op23 real-area update before op11",
			receiver.selectedCharacterID, actor.selectedCharacterID, mode1)
	}
	area, found := wire.actorAreas[actor.selectedCharacterID]
	if !found {
		t.Fatalf("char%d bootstrap lacks captured real area for char%d", receiver.selectedCharacterID, actor.selectedCharacterID)
	}
	areaBody := wire.packets[areaIndex].Body
	if areaBody[2] != area.town || areaBody[3] != area.area {
		t.Fatalf("char%d remote char%d op23 area=%d/%d want captured real area=%d/%d",
			receiver.selectedCharacterID, actor.selectedCharacterID, areaBody[2], areaBody[3], area.town, area.area)
	}
}

func r195PreformationWireSummary(wires []r195PreformationReceiverWire) string {
	const maxFrames = 200
	var summary string
	for _, wire := range wires {
		summary += fmt.Sprintf(" char%d first_op11=%d nodes=", wire.charID, wire.firstOP11)
		limit := min(len(wire.packets), maxFrames)
		for index := 0; index < limit; index++ {
			packet := wire.packets[index]
			mode := ""
			if packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(packet.Body) > 0 {
				mode = fmt.Sprintf("/m%d", packet.Body[0])
			}
			summary += fmt.Sprintf("%d:%d%s(", index, packet.Header.MsgID, mode)
			if packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && len(packet.Body) >= 0x17 {
				summary += fmt.Sprintf("key=%d,owner=%d,len=%d",
					binary.LittleEndian.Uint16(packet.Body[0x15:0x17]), packet.Body[4], len(packet.Body))
			} else if packet.Header.MsgID == currentTownUserLeaveMsgID && len(packet.Body) >= 2 {
				summary += fmt.Sprintf("remove=%d", binary.LittleEndian.Uint16(packet.Body[:2]))
			} else if packet.Header.MsgID == currentTownUserAreaNotificationMsgID && len(packet.Body) >= 4 {
				summary += fmt.Sprintf("key=%d,area=%d", binary.LittleEndian.Uint16(packet.Body[:2]), packet.Body[3])
			}
			summary += ") "
		}
		if len(wire.packets) > maxFrames {
			summary += "..."
		}
	}
	return summary
}
