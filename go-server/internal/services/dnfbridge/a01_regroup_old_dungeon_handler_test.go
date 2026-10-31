package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Old-dungeon return and regroup regression. Requires the integrated
// a01_regroup_first_entry_handler_test.go and the existing dnfbridge fixtures.
// C stays in the old dungeon. The returning member's real op42/op35 already
// removes membership, so the later real op13 is an idempotent leave request.
func TestA01RegroupAfterOldDungeonAbandonThroughHandlers(t *testing.T) {
	for _, roles := range []struct {
		name string
		swap bool
	}{
		{name: "A_abandons_C_joins_B"},
		{name: "B_abandons_C_joins_A", swap: true},
	} {
		t.Run(roles.name, func(t *testing.T) {
			f, oldLeader, conns := a01RegroupFixture(t)
			s := f.service
			// Crossing dungeon -> town settles elapsed pet time even when no
			// pet is equipped. Supply the existing parsed test catalog before
			// entry, without inventing an equipped pet or bypassing the clock.
			catalog, err := dnfpet.NewPVFCatalog(bridgePetCatalogFixture())
			if err != nil {
				t.Fatal(err)
			}
			s.petCatalog = catalog
			if _, _, _, err := s.currentPetGrowthOwner(); err != nil {
				t.Fatalf("pet clock fixture dependency: %v", err)
			}
			leaver, newLeader := f.leader, f.follower
			if roles.swap {
				leaver, newLeader = newLeader, leaver
			}
			t.Setenv(dungeonPartySelectorContextResetEnv, "on")
			// The base fixture starts after login. Supply its omitted login owner
			// context before any party/run handler; never repair it after return.
			for _, session := range []*gameSession{oldLeader, leaver, newLeader} {
				owner, ok := currentOwnerChannelFieldValue(session.channel.ID)
				if !ok || owner == currentSceneObjectContext {
					t.Fatal("fixture channel has no representable nonzero town owner")
				}
				session.connectionTownActorOwnerChannel = owner
				session.townActorOwnerChannel = owner
			}

			// Both parties are created by actual request/accept handlers. No
			// singleton manager.Create, PartyID assignment or injected run.
			oldPartyID := a01OldDungeonInvite(t, s, oldLeader, leaver)
			a01OldDungeonEnter(t, s, oldLeader, leaver, newLeader, oldPartyID, conns)
			oldRuntime := snapshotPartyDungeonLoadingOwner(oldLeader).runtime
			leaverRuntime := snapshotPartyDungeonLoadingOwner(leaver).runtime
			if oldRuntime == nil || leaverRuntime == nil || oldRuntime.partyInstance == nil ||
				oldRuntime.partyInstance != leaverRuntime.partyInstance ||
				oldRuntime.partyInstance.memberCount() != 2 || s.partyDungeonInstanceCount() != 1 {
				t.Fatal("actual old-party op16/op37 did not create one shared two-member instance")
			}
			oldKey, oldKeyOK := s.currentDungeonPartyRunKey(oldLeader)
			memberKey, memberKeyOK := s.currentDungeonPartyRunKey(leaver)
			if !oldKeyOK || !memberKeyOK || oldKey != memberKey || oldKey.PartyID != oldPartyID {
				t.Fatal("old-party handlers did not bind the same authoritative run")
			}

			beforeReturn := testPacketLogText(t, s)
			a01OldDungeonAbandon(t, s, leaver, conns[leaver.selectedCharacterID], leaverRuntime)
			a01OldDungeonAssertSolo(t, s, leaver)
			a01OldDungeonAssertSurvivor(t, s, oldLeader, oldRuntime, oldPartyID)
			// op42 confirmation has already removed the member. Do not recreate
			// membership merely to manufacture a first-removal op13 assertion.
			leaveStart := len(conns[leaver.selectedCharacterID].frames())
			a01RegroupPartyCommand(t, s, leaver, dnfenum.CmdPacketLeaveParty, nil)
			a01RegroupAssertACK(t, a01RegroupFrames(t, conns[leaver.selectedCharacterID], leaveStart), 13, 1)
			a01OldDungeonAssertSolo(t, s, leaver)
			a01OldDungeonAssertSurvivor(t, s, oldLeader, oldRuntime, oldPartyID)
			a01OldDungeonLogSince(t, s, beforeReturn)

			newPartyID := a01OldDungeonInvite(t, s, newLeader, leaver)
			if newPartyID == oldPartyID {
				t.Fatal("new invitation reused C's still-active old party")
			}
			for _, session := range []*gameSession{newLeader, leaver} {
				if snapshotPartyDungeonLoadingOwner(session).runtime != nil {
					t.Fatalf("char=%d retained a runtime before new-party first op16", session.selectedCharacterID)
				}
				if _, ready := s.currentDungeonPartyRunKey(session); ready {
					t.Fatalf("char=%d retained a run key before new-party first op16", session.selectedCharacterID)
				}
			}
			oldReceiveStart := len(conns[oldLeader.selectedCharacterID].frames())
			a01OldDungeonEnter(t, s, newLeader, leaver, oldLeader, newPartyID, conns)
			a01RegroupAssertParty(t, s, newLeader, leaver, newPartyID)
			leaderKey, leaderOK := s.currentDungeonPartyRunKey(newLeader)
			joinerKey, joinerOK := s.currentDungeonPartyRunKey(leaver)
			left := snapshotPartyDungeonLoadingOwner(newLeader).runtime
			right := snapshotPartyDungeonLoadingOwner(leaver).runtime
			if !leaderOK || !joinerOK || leaderKey != joinerKey || leaderKey.PartyID != newPartyID || leaderKey == oldKey ||
				left == nil || right == nil || right == leaverRuntime || left.partyInstance == nil ||
				left.partyInstance != right.partyInstance || left.partyInstance == oldRuntime.partyInstance ||
				left.partyInstance.memberCount() != 2 || s.partyDungeonInstanceCount() != 2 {
				t.Fatal("new-party entry did not keep a separate shared two-member run alongside C's old run")
			}
			a01OldDungeonAssertSurvivor(t, s, oldLeader, oldRuntime, oldPartyID)
			if key, ok := s.currentDungeonPartyRunKey(oldLeader); !ok || key != oldKey {
				t.Fatal("new entry changed C's old run key")
			}
			for i, frame := range a01RegroupFrames(t, conns[oldLeader.selectedCharacterID], oldReceiveStart) {
				for _, actor := range []*gameSession{newLeader, leaver} {
					if a01RegroupPeerPart(frame, actor.selectedCharacterID) != "" {
						t.Errorf("old C received new-party actor=%d at frame=%d op%d", actor.selectedCharacterID, i, frame.msgID)
					}
				}
				if frame.class == 0 && (frame.msgID == 28 || frame.msgID == 29 || frame.msgID == 37 || frame.msgID == 120) {
					t.Errorf("old C received new-party loading frame op%d", frame.msgID)
				}
			}
		})
	}
}

func a01OldDungeonInvite(t *testing.T, s *Service, leader, member *gameSession) uint16 {
	t.Helper()
	before := testPacketLogText(t, s)
	for _, session := range []*gameSession{leader, member} {
		a01OldDungeonAssertSolo(t, s, session)
	}
	if !s.onlinePlayers.PeerInSameArea(leader.selectedCharacterID, member.selectedCharacterID) {
		t.Fatal("invitation requires actual town copresence; do not inject EnterArea after return")
	}
	request := make([]byte, 13)
	binary.LittleEndian.PutUint16(request, member.selectedCharacterID)
	a01RegroupPartyCommand(t, s, leader, dnfenum.CmdPacketRequestPeer, request)
	a01RegroupPartyCommand(t, s, member, dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(leader.selectedCharacterID, 0, 0))
	party, found := s.runtimePartyManagerForService().SnapshotByUser(leader.selectedCharacterID, 0)
	if !found || party.ID == 0 {
		t.Fatal("actual invitation/acceptance did not create a party")
	}
	a01RegroupAssertParty(t, s, leader, member, party.ID)
	a01OldDungeonLogSince(t, s, before)
	return party.ID
}

func a01OldDungeonEnter(t *testing.T, s *Service, leader, member, absent *gameSession, partyID uint16, conns map[uint16]*partyDungeonSyncConn) {
	t.Helper()
	before := testPacketLogText(t, s)
	entryStart := make(map[uint16]int, len(conns))
	for id, conn := range conns {
		entryStart[id] = len(conn.frames())
	}
	request := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(request, 700)
	if err := s.handleDungeonSelectUpper(leader, request); err != nil {
		t.Fatalf("party=%d first op16: %v", partyID, err)
	}
	for _, session := range []*gameSession{leader, member} {
		if !s.partyDungeonPostStartMapPlacementPending(session) {
			t.Fatalf("char=%d not held for actual op37", session.selectedCharacterID)
		}
		a01RegroupAssertUnfinished(t, session)
		frames := a01RegroupFrames(t, conns[session.selectedCharacterID], entryStart[session.selectedCharacterID])
		if a01RegroupFrameIndex(frames, 0, 29) < 0 {
			t.Fatalf("char=%d missing first op29", session.selectedCharacterID)
		}
		a01RegroupAssertHeld(t, frames)
	}
	a01RegroupAssertCycle(t, s, partyID, nil, false)
	// Keep one loading order in both role cases: member first, leader second.
	if err := s.sendFinishLoadingStatus(member, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatalf("party=%d member op37: %v", partyID, err)
	}
	a01RegroupAssertCycle(t, s, partyID, []uint16{member.selectedCharacterID}, false)
	for _, session := range []*gameSession{leader, member} {
		frames := a01RegroupFrames(t, conns[session.selectedCharacterID], entryStart[session.selectedCharacterID])
		wantACK := 0
		if session == member {
			wantACK = 1
		}
		a01RegroupAssertACK(t, frames, 37, wantACK)
		a01RegroupAssertHeld(t, frames)
		a01RegroupAssertUnfinished(t, session)
	}
	if err := s.sendFinishLoadingStatus(leader, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatalf("party=%d leader op37: %v", partyID, err)
	}
	a01OldDungeonReleaseEvents(t, s, before, leader, member)
	a01RegroupAssertCycle(t, s, partyID, []uint16{member.selectedCharacterID, leader.selectedCharacterID}, true)
	for _, receiver := range []*gameSession{leader, member} {
		gates := receiver.finishLoadingGateSnapshot()
		if !gates.State || !gates.Completion || !gates.PostFinish || s.partyDungeonPostStartMapPlacementPending(receiver) {
			t.Fatalf("char=%d incomplete actual loading tail: %+v", receiver.selectedCharacterID, gates)
		}
		peer := member
		if receiver == member {
			peer = leader
		}
		// Reuse the integrated per-receiver identity and ordering assertions.
		a01RegroupAssertEntryWire(t, a01RegroupFrames(t, conns[receiver.selectedCharacterID], entryStart[receiver.selectedCharacterID]), receiver, peer, leader, absent)
	}
}

func a01OldDungeonAbandon(t *testing.T, s *Service, session *gameSession, conn *partyDungeonSyncConn, runtime *runtimeDungeonState) {
	t.Helper()
	if runtime == nil || runtime.Session == nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("abandon requires the actual active runtime from old-party op16/op37")
	}
	start := len(conn.frames())
	if err := s.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatalf("actual op42 abandon: %v", err)
	}
	if snapshotPartyDungeonLoadingOwner(session).runtime != runtime || !runtime.townReturnPending ||
		!runtime.townReturnOp24Sent || runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) ||
		runtime.townReturnAckSent || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("op42 did not retain its active runtime pending actual town evidence")
	}
	frames := a01RegroupFrames(t, conn, start)
	a01RegroupAssertACK(t, frames, uint16(dnfenum.CmdPacketGiveupGame), 0)
	want := []byte{38, 1, 1, 0, 0, 0, 0, 0, 0, 0, 5, 3, 1}
	binary.LittleEndian.PutUint16(want[4:6], session.selectedCharacterID)
	binary.LittleEndian.PutUint16(want[6:8], 450)
	binary.LittleEndian.PutUint16(want[8:10], 234)
	if len(frames) != 1 || frames[0].class != 0 || frames[0].msgID != currentSceneTransitionMsgID || !bytes.Equal(frames[0].body, want) {
		t.Fatalf("op42 did not emit exactly the typed fixture-origin op24: %+v", frames)
	}
	// Exercise the client town-position handler. Never set townReturnPending,
	// detach the runtime, re-enter the presence table or mark town ready here.
	if err := s.handleTownSetUserPosition(session, buildTownPositionRequest(450, 234, 5, 0)); err != nil {
		t.Fatalf("actual town op35 confirmation/actor rebuild (fixture must be complete): %v", err)
	}
	if snapshotPartyDungeonLoadingOwner(session).runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned ||
		session.confirmedDungeonReturnStatePending || !session.returnTownFinishLoadingAckOnly {
		t.Fatal("town confirmation did not finish the real abandon/rebuild lifecycle")
	}
	// The existing town roster publisher lazily invalidates this cached key
	// when the next party is formed. Runtime retirement is the return contract;
	// the caller separately requires no stale key after the actual new invite.
	if key, ready := s.currentDungeonPartyRunKey(session); ready {
		if key.DungeonID != runtime.Dungeon.ID || key.Seed != runtime.Seed {
			t.Fatal("abandon retained a key from an unrelated dungeon")
		}
		t.Log("old run cache retained until the next actual party roster publication")
	}
	session.townPostTransition.mu.Lock()
	stage := session.townPostTransition.stage
	session.townPostTransition.mu.Unlock()
	if stage != currentTownPostTransitionComplete {
		t.Fatalf("town actor/creature/skill tail incomplete: stage=%d", stage)
	}
	if session.enterSelectDungeonSent || session.enterSelectDungeonAckSent || session.enterSelectDungeonContextSent || session.townSelectorOriginBound {
		t.Fatal("old selector context survived actual abandon confirmation")
	}
	a01RegroupAssertACK(t, a01RegroupFrames(t, conn, start), uint16(dnfenum.CmdPacketGiveupGame), 1)
	// Also submit the real return-to-town loading acknowledgement. It must not
	// replay the already rebuilt actor tail before the next dungeon's op16.
	townStart := len(conn.frames())
	if err := s.sendFinishLoadingStatus(session, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
		t.Fatalf("town return op37: %v", err)
	}
	townFrames := a01RegroupFrames(t, conn, townStart)
	a01RegroupAssertACK(t, townFrames, 37, 1)
	for _, frame := range townFrames {
		if frame.class == 0 && (frame.msgID == 37 || frame.msgID == 30 || frame.msgID == 120) {
			t.Errorf("town ACK-only op37 replayed actor tail op%d", frame.msgID)
		}
	}
}

func a01OldDungeonAssertSolo(t *testing.T, s *Service, session *gameSession) {
	t.Helper()
	if _, found := s.runtimePartyManagerForService().SnapshotByUser(session.selectedCharacterID, 0); found {
		t.Fatalf("char=%d unexpectedly retains central membership", session.selectedCharacterID)
	}
	if state := runtimePartyStateSnapshot(session); state.PartyID != 0 {
		t.Fatalf("char=%d retains party projection: %+v", session.selectedCharacterID, state)
	}
}

func a01OldDungeonAssertSurvivor(t *testing.T, s *Service, leader *gameSession, runtime *runtimeDungeonState, partyID uint16) {
	t.Helper()
	if snapshotPartyDungeonLoadingOwner(leader).runtime != runtime || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive ||
		runtime.partyInstance == nil || runtime.partyInstance.memberCount() != 1 {
		t.Fatal("C's actual old run was replaced, abandoned or retained the departing member")
	}
	party, found := s.runtimePartyManagerForService().SnapshotByUser(leader.selectedCharacterID, 0)
	if !found || party.ID != partyID || party.Leader != leader.selectedCharacterID || len(party.Members) != 1 || party.Members[0].UserID != leader.selectedCharacterID {
		t.Fatalf("C's old authoritative singleton was lost: found=%t party=%+v", found, party)
	}
}

func a01OldDungeonLogSince(t *testing.T, s *Service, before string) string {
	t.Helper()
	after := testPacketLogText(t, s)
	if !strings.HasPrefix(after, before) {
		t.Fatal("diagnostic log changed before the phase cursor")
	}
	phase := after[len(before):]
	for _, line := range strings.Split(phase, "\n") {
		if a01DiagnosticHasNonNilError(line) {
			t.Fatalf("handler/fixture diagnostic reports failure: %s", line)
		}
	}
	return phase
}

func a01OldDungeonReleaseEvents(t *testing.T, s *Service, before string, leader, member *gameSession) {
	t.Helper()
	phase := a01OldDungeonLogSince(t, s, before)
	counts := map[uint16]int{}
	for _, line := range strings.Split(phase, "\n") {
		if !strings.Contains(line, "kind=game-party-dungeon-loading-released ") {
			continue
		}
		if !strings.Contains(line, " trigger=last_member_op37 ") || !strings.Contains(line, " release_tail=true ") {
			t.Fatalf("unexpected release path, including timeout fallback: %s", line)
		}
		matched := false
		for _, session := range []*gameSession{leader, member} {
			if strings.Contains(line, fmt.Sprintf(" char_id=%d ", session.selectedCharacterID)) {
				counts[session.selectedCharacterID]++
				matched = true
			}
		}
		if !matched {
			t.Fatalf("unrelated member released in this entry phase: %s", line)
		}
	}
	for _, session := range []*gameSession{leader, member} {
		if counts[session.selectedCharacterID] != 1 {
			t.Fatalf("char=%d phase release count=%d want 1", session.selectedCharacterID, counts[session.selectedCharacterID])
		}
	}
}
