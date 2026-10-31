package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Regrouping and first-entry regression. This file belongs in the existing
// dnfbridge test package (or an overlay), not in a standalone Go module.
// A=99, B=100, C=101. The two role assignments use fresh services, while each
// leaver keeps the same session throughout old-party leave and new-party entry.
func TestA01RegroupFirstEntryThroughHandlers(t *testing.T) {
	for _, roles := range []struct {
		name string
		swap bool
	}{
		{name: "A_leaves_C_joins_B"},
		{name: "B_leaves_C_joins_A", swap: true},
	} {
		t.Run(roles.name, func(t *testing.T) {
			for _, leaderFirst := range []bool{true, false} {
				order := "leaver_op37_first"
				if leaderFirst {
					order = "leader_op37_first"
				}
				t.Run(order, func(t *testing.T) {
					f, oldLeader, conns := a01RegroupFixture(t)
					s := f.service
					leaver, newLeader := f.leader, f.follower
					if roles.swap {
						leaver, newLeader = newLeader, leaver
					}

					// Singleton lobby creation is fixture setup, as in
					// TestPartyJoinRequestKeepsExistingLeaderAndSlots. Both joins
					// and the intervening leave below use the actual handlers.
					oldPartyID := a01RegroupCreateLobby(t, s, oldLeader)
					newPartyID := a01RegroupCreateLobby(t, s, newLeader)
					if oldPartyID == newPartyID {
						t.Fatal("fixture did not create two distinct parties")
					}
					a01RegroupRequestJoin(t, s, leaver, oldLeader)
					a01RegroupAssertParty(t, s, oldLeader, leaver, oldPartyID)

					leaveStart := len(conns[leaver.selectedCharacterID].frames())
					a01RegroupPartyCommand(t, s, leaver, dnfenum.CmdPacketLeaveParty, nil)
					leaveFrames := a01RegroupFrames(t, conns[leaver.selectedCharacterID], leaveStart)
					a01RegroupAssertACK(t, leaveFrames, 13, 1)
					if _, found := s.runtimePartyManagerForService().SnapshotByUser(leaver.selectedCharacterID, 0); found {
						t.Fatal("op13 left the leaver in the central party manager")
					}
					if state := runtimePartyStateSnapshot(leaver); state.PartyID != 0 {
						t.Fatalf("op13 left the leaver's party projection: %+v", state)
					}
					remaining, found := s.runtimePartyManagerForService().SnapshotByUser(oldLeader.selectedCharacterID, 0)
					if !found || remaining.ID != oldPartyID || len(remaining.Members) != 1 || remaining.Members[0].UserID != oldLeader.selectedCharacterID {
						t.Fatalf("old party did not retain only C: found=%t party=%+v", found, remaining)
					}

					a01RegroupRequestJoin(t, s, leaver, newLeader)
					a01RegroupAssertParty(t, s, newLeader, leaver, newPartyID)
					for _, session := range []*gameSession{newLeader, leaver, oldLeader} {
						if s.sessionHasActiveDungeonRuntime(session) {
							t.Fatalf("char %d already had a dungeon before the first op16", session.selectedCharacterID)
						}
					}

					// Capture only the new party's first entry. Do not erase any
					// party/run/projection/finish-loading state to make it pass.
					entryStart := make(map[uint16]int, len(conns))
					for id, conn := range conns {
						entryStart[id] = len(conn.frames())
					}
					request := make([]byte, dungeoncmd.SelectDungeonRequestSize)
					binary.LittleEndian.PutUint32(request, 700)
					if err := s.handleDungeonSelectUpper(newLeader, request); err != nil {
						t.Fatalf("first op16 char=%d: %v", newLeader.selectedCharacterID, err)
					}
					for _, session := range []*gameSession{newLeader, leaver} {
						if !s.partyDungeonPostStartMapPlacementPending(session) {
							t.Fatalf("char %d was not held for real op37", session.selectedCharacterID)
						}
						a01RegroupAssertUnfinished(t, session)
						frames := a01RegroupFrames(t, conns[session.selectedCharacterID], entryStart[session.selectedCharacterID])
						if a01RegroupFrameIndex(frames, 0, 29) < 0 {
							t.Fatalf("char %d received no first start-map op29", session.selectedCharacterID)
						}
						a01RegroupAssertHeld(t, frames)
					}
					a01RegroupAssertCycle(t, s, newPartyID, nil, false)

					first, second := leaver, newLeader
					if leaderFirst {
						first, second = newLeader, leaver
					}
					if err := s.sendFinishLoadingStatus(first, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
						t.Fatalf("first op37 char=%d: %v", first.selectedCharacterID, err)
					}
					a01RegroupAssertCycle(t, s, newPartyID, []uint16{first.selectedCharacterID}, false)
					for _, session := range []*gameSession{first, second} {
						frames := a01RegroupFrames(t, conns[session.selectedCharacterID], entryStart[session.selectedCharacterID])
						wantACK := 0
						if session == first {
							wantACK = 1
						}
						a01RegroupAssertACK(t, frames, 37, wantACK)
						a01RegroupAssertHeld(t, frames)
						a01RegroupAssertUnfinished(t, session)
					}

					// The dispatcher is synchronous, but production still releases
					// members in parallel and waits for both workers. No ready gate
					// is injected and no timeout/force-release fallback is invoked.
					if err := s.sendFinishLoadingStatus(second, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
						t.Fatalf("second op37 char=%d: %v", second.selectedCharacterID, err)
					}
					a01RegroupAssertReleaseEvents(t, s, newLeader, leaver)
					a01RegroupAssertCycle(t, s, newPartyID, []uint16{first.selectedCharacterID, second.selectedCharacterID}, true)
					for _, session := range []*gameSession{newLeader, leaver} {
						gates := session.finishLoadingGateSnapshot()
						if !gates.State || !gates.Completion || !gates.PostFinish || s.partyDungeonPostStartMapPlacementPending(session) {
							t.Fatalf("char %d incomplete actual loading tail: %+v", session.selectedCharacterID, gates)
						}
						peer := leaver
						if session == leaver {
							peer = newLeader
						}
						frames := a01RegroupFrames(t, conns[session.selectedCharacterID], entryStart[session.selectedCharacterID])
						a01RegroupAssertEntryWire(t, frames, session, peer, newLeader, oldLeader)
					}
					a01RegroupAssertSameRun(t, s, newLeader, leaver, oldLeader, newPartyID)
					a01RegroupAssertParty(t, s, newLeader, leaver, newPartyID)
					oldFrames := a01RegroupFrames(t, conns[oldLeader.selectedCharacterID], entryStart[oldLeader.selectedCharacterID])
					for i, frame := range oldFrames {
						for _, peer := range []*gameSession{newLeader, leaver} {
							if a01RegroupPeerPart(frame, peer.selectedCharacterID) != "" {
								t.Errorf("old C received new-party actor %d at frame %d: op%d", peer.selectedCharacterID, i, frame.msgID)
							}
						}
						if frame.class == 0 && (frame.msgID == 28 || frame.msgID == 29 || frame.msgID == 37 || frame.msgID == 120) {
							t.Errorf("old C received a new-party loading frame: op%d", frame.msgID)
						}
					}
				})
			}
		})
	}
}

func a01RegroupFixture(t *testing.T) (*inviteAcceptDungeonFixture, *gameSession, map[uint16]*partyDungeonSyncConn) {
	t.Helper()
	f := newInviteAcceptDungeonFixture(t)
	s := f.service
	prepareTestCharacterInitialization(s, 15)
	// Dungeon placement now evaluates the account's crystal contract. These
	// synthetic accounts have no contract, but its catalog must still be valid.
	s.premiumCatalog = &currentPremiumCatalog{
		contractsByItem: make(map[int64]currentPremiumContractInfo),
		devilSlots:      make(map[uint32]currentPremiumDevilSlotInfo),
		crystalCubeIDs:  [6]int64{3033, 3034, 3035, 3036, 3037, 3262},
	}
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	// This isolated handler test has no inventory items. Supply a parsed,
	// explicitly empty catalog so normal login cleanup retains its real path.
	items, itemErr := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "", "equipment/equipment.lst": "",
	})
	if itemErr != nil {
		t.Fatal(itemErr)
	}
	s.pvfItemCatalog = items
	s.options.currentClientVersion = "official-24.9"
	s.options.partyDungeonEntryBarrier = partyDungeonEntryBarrierVendor
	s.options.partyDungeonLoadingBarrier = partyDungeonLoadingBarrierVendor
	s.options.partyDungeonOwner = partyDungeonOwnerSceneContext
	t.Setenv(dungeonPartyPeerActorLandingResendEnv, "on")

	// Reuse the existing synthetic job15 skill fixture from
	// TestFinishLoadingStatusThenMainCharacterStateUsesRealRecord. This enables
	// the real op19/post-finish path, not a preset PostFinish flag or a stub.
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Inventory == nil || repos.Skill == nil {
		t.Fatal("invite fixture has no complete memory character/inventory/skill stores")
	}
	ctx := context.Background()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1", State: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "101", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "101", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"99", "100", "101"} {
		character, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load synthetic character %s: found=%t err=%v", id, found, err)
		}
		character.Job, character.Name = "15", "a01-"+id
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
			CharacterID: id,
			Points: dnfrepo.SkillPointState{
				TotalSP: 11, RemainingSP: 11, TotalTP: 14, RemainingTP: 14,
				SyncedLevel: character.Level,
			},
			Skills: map[int64]dnfrepo.SkillState{1: {Level: 1, Enabled: true}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst":     "15 `job15.lst`\n",
		"skill/job15.lst":         "1 `job15/one.skl`\n",
		"skill/job15/one.skl":     "[skill type]\n`active`\n[skill class]\n1\n[required level]\n1\n",
		"character/character.lst": "15 `job15.chr`\n",
		"character/job15.chr":     "[job]\n`[gunblader]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.initialSkillsByJob = map[byte][]initialSkillEntry{15: {{SkillID: 1, Level: 1}}}
	s.initialSPTable, s.initialTPTable = map[int]int{1: 11}, map[int]int{1: 14}
	s.skillCatalog = catalog

	oldLeader := &gameSession{connID: "a01-C", channel: f.channel, residentChannel: f.channel}
	conns := make(map[uint16]*partyDungeonSyncConn, 3)
	for id, session := range map[uint16]*gameSession{99: f.leader, 100: f.follower, 101: oldLeader} {
		// The synchronized stream buffer is reused; its global trace is not
		// used to impose an order between independent receiver connections.
		conn := &partyDungeonSyncConn{charID: id}
		conns[id], session.conn = conn, conn
		session.connID = fmt.Sprintf("a01-%d", id)
	}
	s.bindGameSessionCharacter(oldLeader, 101)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 101, TownID: 38, AreaID: 1, Session: oldLeader})
	bindDungeonSelectorOriginForTestAt(t, s, oldLeader, 38, 1, 450, 234)

	previousDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() {
		s.partyDungeonLoadingMu.Lock()
		for _, cycle := range s.partyDungeonLoadingCycles {
			if cycle != nil && cycle.Timer != nil {
				cycle.Timer.Stop()
			}
		}
		s.partyDungeonLoadingMu.Unlock()
		partyDungeonLoadingBarrierDispatch = previousDispatch
	})
	return f, oldLeader, conns
}

func a01RegroupCreateLobby(t *testing.T, s *Service, leader *gameSession) uint16 {
	t.Helper()
	identity, ok := s.boundGameSessionCharacterSnapshot(leader)
	if !ok {
		t.Fatal("unbound singleton lobby owner")
	}
	member, ok := s.runtimePartyMemberForIdentity(identity)
	if !ok {
		t.Fatal("cannot construct singleton lobby member")
	}
	created := s.runtimePartyManagerForService().Create(member, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatalf("create singleton lobby: %s", created.Reason)
	}
	s.publishRuntimePartySnapshot(created.Party)
	return created.Party.ID
}

func a01RegroupPartyCommand(t *testing.T, s *Service, session *gameSession, cmd dnfenum.CmdPacket, body []byte) {
	t.Helper()
	handled, err := s.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(cmd), body)
	if err != nil || !handled {
		t.Fatalf("party op%d char=%d: handled=%t err=%v", cmd, session.selectedCharacterID, handled, err)
	}
}

func a01RegroupRequestJoin(t *testing.T, s *Service, joiner, anchor *gameSession) {
	t.Helper()
	request := make([]byte, 13)
	binary.LittleEndian.PutUint16(request, anchor.selectedCharacterID)
	a01RegroupPartyCommand(t, s, joiner, dnfenum.CmdPacketRequestPeer, request)
	a01RegroupPartyCommand(t, s, anchor, dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(joiner.selectedCharacterID, 0, 0))
}

func a01RegroupAssertParty(t *testing.T, s *Service, leader, member *gameSession, partyID uint16) {
	t.Helper()
	for _, session := range []*gameSession{leader, member} {
		snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(session.selectedCharacterID, 0)
		if !found || snapshot.ID != partyID || snapshot.Leader != leader.selectedCharacterID || len(snapshot.Members) != 2 {
			t.Fatalf("central party for char %d: found=%t snapshot=%+v", session.selectedCharacterID, found, snapshot)
		}
		for _, entry := range snapshot.Members {
			wantSlot := byte(0)
			if entry.UserID == member.selectedCharacterID {
				wantSlot = 1
			} else if entry.UserID != leader.selectedCharacterID {
				t.Fatalf("unexpected central member %d", entry.UserID)
			}
			if entry.Slot != wantSlot {
				t.Fatalf("char %d slot=%d want=%d", entry.UserID, entry.Slot, wantSlot)
			}
		}
		state := runtimePartyStateSnapshot(session)
		rows := runtimePartyMembers(state)
		if state.PartyID != int(partyID) || state.UserID != leader.selectedCharacterID || len(rows) != 2 || rows[0].UserID != leader.selectedCharacterID || rows[1].UserID != member.selectedCharacterID {
			t.Fatalf("session %d retained a stale party projection: %+v", session.selectedCharacterID, state)
		}
	}
}

type a01RegroupFrame struct {
	class byte
	msgID uint16
	body  []byte
}

func a01RegroupFrames(t *testing.T, conn *partyDungeonSyncConn, start int) []a01RegroupFrame {
	t.Helper()
	data := conn.frames()
	if start < 0 || start > len(data) {
		t.Fatalf("invalid stream cursor %d for %d bytes", start, len(data))
	}
	var frames []a01RegroupFrame
	for rest := data[start:]; len(rest) > 0; {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
		if len(next) >= len(rest) {
			t.Fatal("packet parser made no progress")
		}
		frames = append(frames, a01RegroupFrame{packet.Header.Classification, packet.Header.MsgID, packet.Body})
		rest = next
	}
	return frames
}

func a01RegroupFrameIndex(frames []a01RegroupFrame, class byte, msgID uint16) int {
	for i, frame := range frames {
		if frame.class == class && frame.msgID == msgID {
			return i
		}
	}
	return -1
}

func a01RegroupAssertACK(t *testing.T, frames []a01RegroupFrame, msgID uint16, want int) {
	t.Helper()
	count := 0
	for _, frame := range frames {
		if frame.class == 1 && frame.msgID == msgID {
			count++
			if !bytes.Equal(frame.body, []byte{1}) {
				t.Errorf("op%d ACK body=%x want success discriminator only", msgID, frame.body)
			}
		}
	}
	if count != want {
		t.Fatalf("op%d ACK count=%d want=%d", msgID, count, want)
	}
}

func a01RegroupAssertHeld(t *testing.T, frames []a01RegroupFrame) {
	t.Helper()
	for _, frame := range frames {
		if frame.class == 0 && (frame.msgID == 37 || (frame.msgID == 120 && len(frame.body) == 2)) {
			t.Fatalf("loading released before both real op37 requests: op%d", frame.msgID)
		}
	}
}

func a01RegroupAssertUnfinished(t *testing.T, session *gameSession) {
	t.Helper()
	if gates := session.finishLoadingGateSnapshot(); gates.State || gates.Completion || gates.PostFinish {
		t.Fatalf("char %d unexpectedly has completed loading gates: %+v", session.selectedCharacterID, gates)
	}
}

func a01RegroupAssertCycle(t *testing.T, s *Service, partyID uint16, readyIDs []uint16, released bool) {
	t.Helper()
	s.partyDungeonLoadingMu.Lock()
	defer s.partyDungeonLoadingMu.Unlock()
	cycle := s.partyDungeonLoadingCycles[partyID]
	if cycle == nil || cycle.Cycle != 1 || cycle.Released != released || len(cycle.membersAt) != 2 {
		t.Fatalf("first-entry cycle mismatch: %+v want released=%t", cycle, released)
	}
	want := make(map[uint16]bool, len(readyIDs))
	for _, id := range readyIDs {
		want[id] = true
	}
	for _, id := range cycle.membersAt {
		if cycle.Ready[id] != want[id] {
			t.Errorf("cycle readiness char=%d got=%t want=%t", id, cycle.Ready[id], want[id])
		}
	}
	for id, ready := range cycle.Ready {
		if ready && !want[id] {
			t.Errorf("unexpected ready member %d", id)
		}
	}
}

// Same offsets as r160UpperPeerActorCounts. USERINFO modes have different key
// fields: do not apply op9's body[4:6] to mode0/mode1 or count mode8 aux data.
func a01RegroupPeerPart(frame a01RegroupFrame, peer uint16) string {
	if frame.class != 0 {
		return ""
	}
	body := frame.body
	if frame.msgID == 2 && len(body) > 0 {
		if body[0] == 0 && len(body) >= 78 && binary.LittleEndian.Uint16(body[76:78]) == peer {
			return "mode0"
		}
		if body[0] == 1 && len(body) >= 23 && binary.LittleEndian.Uint16(body[21:23]) == peer {
			return "mode1"
		}
	}
	if frame.msgID == 9 && len(body) >= 12 && binary.LittleEndian.Uint16(body[:2]) == 1 &&
		binary.LittleEndian.Uint16(body[2:4]) == currentSceneOp9StableSceneValue &&
		binary.LittleEndian.Uint16(body[4:6]) == peer && body[9] == currentSceneOp9ActorDisplayKind {
		return "op9"
	}
	return ""
}

func a01RegroupAssertEntryWire(t *testing.T, frames []a01RegroupFrame, receiver, peer, leader, oldLeader *gameSession) {
	t.Helper()
	a01RegroupAssertACK(t, frames, 37, 1)
	startMap := a01RegroupFrameIndex(frames, 0, 29)
	ack := a01RegroupFrameIndex(frames, 1, 37)
	mainState := a01RegroupFrameIndex(frames, 0, 37)
	completion := a01RegroupFrameIndex(frames, 0, 30)
	skills := a01RegroupFrameIndex(frames, 0, currentSkillInfoMsgID)
	if startMap < 0 || ack <= startMap || mainState <= ack || completion <= mainState || skills <= completion {
		t.Fatalf("receiver=%d first-entry order op29/ACK37/main37/op30/op19=%d/%d/%d/%d/%d",
			receiver.selectedCharacterID, startMap, ack, mainState, completion, skills)
	}
	if len(frames[mainState].body) != currentFinishLoadingCharacterStateBodySize ||
		!bytes.Equal(frames[completion].body, make([]byte, currentIncreaseStatusResultBodySize)) {
		t.Fatal("loading main-state/completion body does not match the existing wire contract")
	}
	initialPlacement := false
	for _, frame := range frames[ack+1 : mainState] {
		if frame.class == 0 && frame.msgID == 120 && bytes.Equal(frame.body, []byte{0, 0}) {
			initialPlacement = true
		}
	}
	if !initialPlacement {
		t.Fatal("deferred first placement did not occur between op37 ACK and main state")
	}
	if body := frames[skills].body; len(body) < 4 || int(binary.LittleEndian.Uint32(body[:4])) != len(body)-4 {
		t.Fatal("real post-finish op19 is missing its length-delimited skill payload")
	}
	postFinishPlacement := -1
	for i := skills + 1; i < len(frames); i++ {
		if frame := frames[i]; frame.class == 0 && frame.msgID == 120 && bytes.Equal(frame.body, []byte{0, 0}) {
			postFinishPlacement = i
			break
		}
	}
	if postFinishPlacement < 0 {
		t.Fatal("post-finish placement did not follow real op19")
	}

	// Validate all peer roster records, then require a complete
	// ordered peer creation triple after THIS receiver's actual loading tail.
	// Initial projection and resend may coexist; no global exact frame count
	// or fixed order between the two receiving sockets is assumed.
	mode0, mode1, roster := -1, -1, -1
	for i, frame := range frames {
		if a01RegroupPeerPart(frame, oldLeader.selectedCharacterID) != "" {
			t.Errorf("receiver=%d got old C actor during new entry at frame %d", receiver.selectedCharacterID, i)
		}
		part := a01RegroupPeerPart(frame, peer.selectedCharacterID)
		if part == "op9" {
			if frame.body[7] != currentSceneObjectContext || currentClientSelectedChannel(receiver) == currentSceneObjectContext {
				t.Fatalf("peer op9 has wrong dungeon owner/client channel: owner=%d channel=%d", frame.body[7], currentClientSelectedChannel(receiver))
			}
			rows, leaderSlot := a01RegroupOfficialPartyRows(t, frame.body)
			followerID := peer.selectedCharacterID
			if followerID == leader.selectedCharacterID {
				followerID = receiver.selectedCharacterID
			}
			if len(rows) != 2 || leaderSlot != 0 || rows[0].uid != leader.selectedCharacterID || rows[1].uid != followerID {
				t.Fatalf("peer op9 retained wrong members/leader: rows=%+v leaderSlot=%d", rows, leaderSlot)
			}
			for slot, row := range rows {
				if row.slot != byte(slot) || !row.hasIdentity || row.job != 15 || row.level != 20 || row.name != fmt.Sprintf("a01-%d", row.uid) {
					t.Fatalf("peer op9 identity/slot mismatch: %+v", row)
				}
			}
		}
		if i <= postFinishPlacement {
			continue
		}
		switch part {
		case "mode0":
			if mode0 < 0 {
				mode0 = i
			}
		case "mode1":
			if mode0 >= 0 && mode1 < 0 {
				mode1 = i
			}
		case "op9":
			if mode1 >= 0 && roster < 0 {
				roster = i
			}
		}
	}
	if mode0 < 0 || mode1 <= mode0 || roster <= mode1 {
		t.Fatalf("receiver=%d peer=%d lacks mode0->mode1->op9 after post-finish op120=%d: %d/%d/%d",
			receiver.selectedCharacterID, peer.selectedCharacterID, postFinishPlacement, mode0, mode1, roster)
	}
}

// Official 24.9 has an additional actor metadata byte, one extra member byte,
// and a secondary identity string/flag. The older parseOp9PartyFrame helper
// deliberately parses the legacy layout and must not be used for this client.
func a01RegroupOfficialPartyRows(t *testing.T, body []byte) ([]op9PartyRow, byte) {
	t.Helper()
	offset := 0
	take := func(n int) []byte {
		if n < 0 || n > len(body)-offset {
			t.Fatalf("official op9 truncated at %d size=%d total=%d", offset, n, len(body))
		}
		value := body[offset : offset+n]
		offset += n
		return value
	}
	str := func() string {
		n := binary.LittleEndian.Uint32(take(4))
		if uint64(n) > uint64(len(body)-offset) {
			t.Fatalf("official op9 invalid string length %d at %d", n, offset)
		}
		return string(take(int(n)))
	}
	header := take(12)
	if binary.LittleEndian.Uint16(header[:2]) != 1 || binary.LittleEndian.Uint16(header[2:4]) != currentSceneOp9StableSceneValue || header[9] != currentSceneOp9ActorDisplayKind {
		t.Fatal("official op9 header mismatch")
	}
	_ = str()
	metadata := take(14)
	if metadata[13] != 0 {
		t.Fatal("unexpected official actor metadata extension")
	}
	count := int(take(1)[0])
	if count != 2 {
		t.Fatalf("official op9 member count=%d want 2", count)
	}
	rows := make([]op9PartyRow, 0, count)
	for i := 0; i < count; i++ {
		identity := take(8) // slot, key, job, level, grow, flag, extra flag
		row := op9PartyRow{slot: identity[0], uid: binary.LittleEndian.Uint16(identity[1:3]),
			hasIdentity: true, job: identity[3], level: identity[4], grow: identity[5]}
		if identity[6] != 0 || identity[7] != 0 {
			t.Fatal("unexpected official member identity flags")
		}
		row.name = str()
		if secondary := str(); secondary != "" || take(1)[0] != 0 {
			t.Fatal("unexpected secondary member identity")
		}
		state := take(3)
		if state[0] != 0 || state[2] != 0 {
			t.Fatal("unexpected party member state flags")
		}
		row.state = state[1]
		rows = append(rows, row)
	}
	tail := take(4)
	if tail[0] != 0 || tail[2] != 0 || tail[3] != 0 || offset != len(body) {
		t.Fatal("unexpected official op9 tail")
	}
	return rows, tail[1]
}

func a01RegroupAssertReleaseEvents(t *testing.T, s *Service, leader, member *gameSession) {
	t.Helper()
	log := testPacketLogText(t, s)
	if log == "" {
		t.Fatal("fixture has no release diagnostics; nil outer op37 errors are insufficient")
	}
	counts := map[uint16]int{}
	for _, line := range strings.Split(log, "\n") {
		// Production's void release/peer-projection wrappers log errors. They
		// must not be mistaken for a successful nil from sendFinishLoadingStatus.
		if a01DiagnosticHasNonNilError(line) {
			t.Errorf("handler/fixture diagnostic contains an error: %s", line)
		}
		if !strings.Contains(line, "kind=game-party-dungeon-loading-released ") {
			continue
		}
		if !strings.Contains(line, " trigger=last_member_op37 ") || !strings.Contains(line, " release_tail=true ") {
			t.Errorf("unexpected release path (including watchdog fallback): %s", line)
		}
		matched := false
		for _, session := range []*gameSession{leader, member} {
			if strings.Contains(line, fmt.Sprintf(" char_id=%d ", session.selectedCharacterID)) {
				counts[session.selectedCharacterID]++
				matched = true
			}
		}
		if !matched {
			t.Errorf("unrelated member released: %s", line)
		}
	}
	for _, session := range []*gameSession{leader, member} {
		if got := counts[session.selectedCharacterID]; got != 1 {
			t.Errorf("char=%d release events=%d want exactly one", session.selectedCharacterID, got)
		}
	}
}

func a01DiagnosticHasNonNilError(line string) bool {
	const marker = " error="
	for {
		offset := strings.Index(line, marker)
		if offset < 0 {
			return false
		}
		value := line[offset+len(marker):]
		if !strings.HasPrefix(value, "<nil>") {
			return true
		}
		tail := value[len("<nil>"):]
		if tail != "" && tail[0] != ' ' && tail[0] != '\t' && tail[0] != '\r' && tail[0] != '\n' {
			return true
		}
		line = tail
	}
}

func TestA01DiagnosticRejectsRealErrorsAfterNil(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"EVENT ok", false}, {"EVENT error=<nil>", false},
		{"EVENT error=<nil> next=1 error=<nil>", false},
		{"EVENT error=failed", true}, {"EVENT error=", true},
		{"EVENT error=<nil>suffix", true},
		{"EVENT error=<nil> next=1 error=failed", true},
	} {
		if got := a01DiagnosticHasNonNilError(tc.line); got != tc.want {
			t.Fatalf("diagnostic %q error=%t, want %t", tc.line, got, tc.want)
		}
	}
}

func a01RegroupAssertSameRun(t *testing.T, s *Service, leader, member, oldLeader *gameSession, partyID uint16) {
	t.Helper()
	leaderKey, leaderOK := s.currentDungeonPartyRunKey(leader)
	memberKey, memberOK := s.currentDungeonPartyRunKey(member)
	if !leaderOK || !memberOK || leaderKey != memberKey || leaderKey.PartyID != partyID {
		t.Fatalf("new party does not share its own run: leader=%+v/%t member=%+v/%t", leaderKey, leaderOK, memberKey, memberOK)
	}
	left := snapshotPartyDungeonLoadingOwner(leader)
	right := snapshotPartyDungeonLoadingOwner(member)
	if left.runtime == nil || right.runtime == nil || left.runtime.partyInstance == nil ||
		left.runtime.partyInstance != right.runtime.partyInstance || left.runtime.partyInstance.memberCount() != 2 ||
		s.partyDungeonInstanceCount() != 1 {
		t.Fatal("first entry did not create exactly one shared two-member instance")
	}
	if s.sessionHasActiveDungeonRuntime(oldLeader) {
		t.Fatal("old C was pulled into the new dungeon")
	}
	if _, ready := s.currentDungeonPartyRunKey(oldLeader); ready {
		t.Fatal("old C acquired a dungeon run key")
	}
	remaining, found := s.runtimePartyManagerForService().SnapshotByUser(oldLeader.selectedCharacterID, 0)
	if !found || remaining.ID == partyID || len(remaining.Members) != 1 || remaining.Members[0].UserID != oldLeader.selectedCharacterID {
		t.Fatalf("new entry changed old C's membership: found=%t party=%+v", found, remaining)
	}
}
