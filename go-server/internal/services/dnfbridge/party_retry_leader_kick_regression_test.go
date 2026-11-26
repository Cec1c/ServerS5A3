package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func armCompletedTownReturnUnsettledForTest(t *testing.T, session *gameSession) {
	t.Helper()
	session.townMu.Lock()
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.sceneBootstrapTailDeferred = true
	session.sceneBootstrapTailSent = false
	session.initialTownLegacySceneReadyAccepted = false
	armCurrentCompletedTownSceneTailOwnerLocked(session, session.selectedCharacterID)
	session.townMu.Unlock()
	if !currentCompletedTownReturnPendingSettlement(session) {
		t.Fatal("failed to arm unsettled completed-town return state")
	}
}

func TestCompletedTownReturnUnsettledBlocksPartyEntryUntilClientConfirmsTown(t *testing.T) {
	for _, settleBy := range []string{"op35_position", "op37_ack_only"} {
		t.Run(settleBy, func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			s := f.service
			t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
			f.formPartyThroughClientAccept(t)

			armCompletedTownReturnUnsettledForTest(t, f.leader)

			if ready, reason := s.currentTownEnterSelectReady(f.leader); ready || reason != currentTownEnterSelectReasonCompletedReturnTailPending {
				t.Fatalf("currentTownEnterSelectReady(leader) = (%t, %q), want (false, %q)", ready, reason, currentTownEnterSelectReasonCompletedReturnTailPending)
			}

			// Direct leader op16 must be refused without committing any dungeon runtime.
			f.leader.conn.(*bufferConn).write.Reset()
			f.follower.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
				t.Fatalf("direct leader op16 during unsettled return errored: %v", err)
			}
			for _, member := range []*gameSession{f.leader, f.follower} {
				member.dungeon.mu.Lock()
				hasRuntime := member.dungeon.runtime != nil
				member.dungeon.mu.Unlock()
				if hasRuntime {
					t.Fatalf("char %d committed dungeon runtime while leader completed-town return was unsettled", member.selectedCharacterID)
				}
			}
			if got := partyDungeonPreflightErrorKind(errors.New("party member 2 selector is not ready: " + currentTownEnterSelectReasonCompletedReturnTailPending)); got != "member_town_return_unsettled" {
				t.Fatalf("partyDungeonPreflightErrorKind = %q, want member_town_return_unsettled", got)
			}

			// Follower-routed op16 while leader is unsettled must also be refused safely.
			f.leader.conn.(*bufferConn).write.Reset()
			f.follower.conn.(*bufferConn).write.Reset()
			routed := s.bindPartyDungeonEntryRoutedPending(f.follower)
			if err := s.handleDungeonSelectUpperPlannedWithNativeAdmission(f.leader, nativeAdmissionBody(), nil, true, routed, false, nil); err != nil {
				t.Fatalf("routed op16 during unsettled return errored: %v", err)
			}
			for _, member := range []*gameSession{f.leader, f.follower} {
				member.dungeon.mu.Lock()
				hasRuntime := member.dungeon.runtime != nil
				member.dungeon.mu.Unlock()
				if hasRuntime {
					t.Fatalf("char %d committed dungeon runtime on routed op16 while leader return was unsettled", member.selectedCharacterID)
				}
			}
			// Party must remain intact.
			if state := s.runtimePartyStateForSession(f.leader); state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
				t.Fatalf("party broken after unsettled refusal: %+v", state)
			}

			// Now client proves town landing via op35 or town op37.
			switch settleBy {
			case "op35_position":
				posBody := make([]byte, 7)
				binary.LittleEndian.PutUint16(posBody[0:2], 250)
				binary.LittleEndian.PutUint16(posBody[2:4], 180)
				if err := s.handleTownSetUserPosition(f.leader, posBody); err != nil {
					t.Fatalf("handleTownSetUserPosition: %v", err)
				}
			case "op37_ack_only":
				f.leader.returnTownFinishLoadingAckOnly = true
				f.leader.confirmedDungeonReturnStatePending = false
				if err := s.sendFinishLoadingStatus(f.leader, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
					t.Fatalf("sendFinishLoadingStatus: %v", err)
				}
			}

			if currentCompletedTownReturnPendingSettlement(f.leader) {
				t.Fatalf("completed-town return still unsettled after %s", settleBy)
			}
			if ready, reason := s.currentTownEnterSelectReady(f.leader); !ready {
				t.Fatalf("currentTownEnterSelectReady(leader) = (false, %q) after %s, want ready", reason, settleBy)
			}

			// Subsequent party dungeon entry must now succeed for both members.
			if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
				t.Fatalf("party entry after town settlement failed: %v", err)
			}
			for _, member := range []*gameSession{f.leader, f.follower} {
				member.dungeon.mu.Lock()
				hasRuntime := member.dungeon.runtime != nil
				member.dungeon.mu.Unlock()
				if !hasRuntime {
					t.Fatalf("char %d did not enter dungeon after town settlement", member.selectedCharacterID)
				}
			}
		})
	}
}

func TestStaleBackToVillageAndGiveupBlockedDuringInitialPartyLoadingBarrier(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
	t.Setenv(partyDungeonLoadingBarrierEnv, partyDungeonLoadingBarrierValueVendor)
	f.formPartyThroughClientAccept(t)

	if err := s.handleDungeonSelectUpper(f.leader, nativeAdmissionBody()); err != nil {
		t.Fatalf("party dungeon entry failed: %v", err)
	}

	f.leader.dungeon.mu.Lock()
	leaderRuntime := f.leader.dungeon.runtime
	f.leader.dungeon.mu.Unlock()
	if leaderRuntime == nil || leaderRuntime.partyInstance == nil {
		t.Fatal("leader did not enter a shared party dungeon runtime")
	}
	if !s.partyDungeonPostStartMapPlacementPending(f.leader) {
		t.Fatal("expected pendingPostStartMapPlacement to be armed during initial party loading")
	}

	// 1. Stale op132 (Back2Village) from the previous town/selector screen must be blocked.
	if err := s.handleDungeonBackToVillage(f.leader, nil); err != nil {
		t.Fatalf("handleDungeonBackToVillage returned error: %v", err)
	}
	f.leader.dungeon.mu.Lock()
	if f.leader.dungeon.runtime != leaderRuntime || leaderRuntime.townReturnPending {
		f.leader.dungeon.mu.Unlock()
		t.Fatalf("stale op132 mutated active party loading runtime: runtime=%p townReturnPending=%t", f.leader.dungeon.runtime, leaderRuntime.townReturnPending)
	}
	f.leader.dungeon.mu.Unlock()

	// 2. Stale op42 (GiveupGame) must also be blocked.
	if err := s.handleDungeonGiveupGame(f.leader, nil); err != nil {
		t.Fatalf("handleDungeonGiveupGame returned error: %v", err)
	}
	f.leader.dungeon.mu.Lock()
	if f.leader.dungeon.runtime != leaderRuntime || leaderRuntime.townReturnPending {
		f.leader.dungeon.mu.Unlock()
		t.Fatalf("stale op42 mutated active party loading runtime: runtime=%p townReturnPending=%t", f.leader.dungeon.runtime, leaderRuntime.townReturnPending)
	}
	f.leader.dungeon.mu.Unlock()

	// 3. Defense-in-depth: even if townReturnPending were armed with op132, a stale town
	// confirmation packet must reset the pending attempt without abandoning or kicking leader.
	f.leader.dungeon.mu.Lock()
	leaderRuntime.townReturnPending = true
	leaderRuntime.townReturnRequestMsgID = uint16(dnfenum.CmdPacketBack2Village)
	f.leader.dungeon.mu.Unlock()
	if err := s.commitPendingDungeonReturnForExpectedSceneRequest(f.leader, "test_stale_town_confirm", leaderRuntime); err != nil {
		t.Fatalf("commitPendingDungeonReturnForExpectedSceneRequest returned error: %v", err)
	}
	f.leader.dungeon.mu.Lock()
	if f.leader.dungeon.runtime != leaderRuntime || leaderRuntime.townReturnPending {
		f.leader.dungeon.mu.Unlock()
		t.Fatalf("defense-in-depth did not protect initial loading runtime: runtime=%p townReturnPending=%t", f.leader.dungeon.runtime, leaderRuntime.townReturnPending)
	}
	if status := leaderRuntime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunActive {
		f.leader.dungeon.mu.Unlock()
		t.Fatalf("runtime run status changed to %v during initial loading fence", status)
	}
	f.leader.dungeon.mu.Unlock()

	// Party must still have both members with leader intact.
	if state := s.runtimePartyStateForSession(f.leader); state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 || state.UserID != f.leader.selectedCharacterID {
		t.Fatalf("leader was kicked or party altered during initial loading fence: %+v", state)
	}

	// 4. Once initial loading barrier releases, genuine op132 must work normally.
	f.leader.pendingPostStartMapMu.Lock()
	f.leader.pendingPostStartMapPlacement = nil
	f.leader.pendingPostStartMapMu.Unlock()
	f.leader.setFinishLoadingGateState(true)

	if err := s.handleDungeonBackToVillage(f.leader, nil); err != nil {
		t.Fatalf("genuine op132 after loading release failed: %v", err)
	}
	f.leader.dungeon.mu.Lock()
	pendingAfterLoad := leaderRuntime.townReturnPending
	f.leader.dungeon.mu.Unlock()
	if !pendingAfterLoad {
		t.Fatal("genuine op132 after loading release did not arm townReturnPending")
	}
}

func TestLatePartyEntryPreflightFailureEmitsZeroFollowerSelectorPackets(t *testing.T) {
	f, p := nativeAdmissionProofFixture(t)
	s := f.service

	// Mark proof ready & expired right before commit so prepareRuntimePartyDungeonEntry
	// succeeds but the late nativeProof recheck at preflight rejects entry.
	p.ready.Store(true)
	p.deadline = time.Now().Add(-time.Second)
	p.commitDeadlineUnixNano.Store(time.Now().Add(-time.Second).UnixNano())

	// Also test with a valid proof whose nativeProofCheck fails right after prepareRuntimePartyDungeonEntry:
	// first pass deferPartyNativeConnectionAdmission by using a proof with valid deadline,
	// then break leader town return origin via empty persisted location? No — simpler:
	// we can make freezeCurrentDungeonTownReturnOrigin fail by clearing leader townPositionSnapshot
	// AND clearing leader's persisted town_id in the repository!
	f2 := newInviteAcceptDungeonFixture(t)
	s2 := f2.service
	s2.options.currentClientVersion = "official-24.9"
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "off")
	f2.formPartyThroughClientAccept(t)

	// Invalidate leader's town return origin so prepareRuntimePartyDungeonEntry succeeds
	// for follower, then freezeCurrentDungeonTownReturnOrigin fails on leader (late refusal).
	f2.leader.townMu.Lock()
	f2.leader.townPositionSnapshot.CharacterID = 0
	f2.leader.townPositionSnapshot.PositionValid = false
	f2.leader.townMu.Unlock()
	repos, ok := s2.repositoryGroup()
	if !ok || repos.Character == nil {
		t.Fatal("missing character repository")
	}
	charRec, found, err := repos.Character.Load(context.Background(), strconv.Itoa(int(f2.leader.selectedCharacterID)))
	if err != nil || !found {
		t.Fatalf("load leader char record: found=%t err=%v", found, err)
	}
	charRec.Stats["town_id"] = 0
	charRec.Stats["area_id"] = 0
	if err := repos.Character.Save(context.Background(), charRec); err != nil {
		t.Fatalf("save leader char record: %v", err)
	}

	f2.follower.conn.(*bufferConn).write.Reset()
	f2.leader.conn.(*bufferConn).write.Reset()
	if err := s2.handleDungeonSelectUpper(f2.leader, nativeAdmissionBody()); err != nil {
		t.Fatalf("handleDungeonSelectUpper on late preflight failure returned error: %v", err)
	}

	// Verify neither member entered dungeon.
	for _, member := range []*gameSession{f2.leader, f2.follower} {
		member.dungeon.mu.Lock()
		hasRuntime := member.dungeon.runtime != nil
		member.dungeon.mu.Unlock()
		if hasRuntime {
			t.Fatalf("char %d unexpectedly committed runtime after late preflight failure", member.selectedCharacterID)
		}
	}

	// Critical P1-1 assertion: follower must NOT have received op15 / op36 / op27 selector packets.
	frames := splitUpperFrames(t, f2.follower.conn.(*bufferConn).write.Bytes())
	for _, frame := range frames {
		if frame.msgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) ||
			frame.msgID == currentFatigueMsgID ||
			frame.msgID == 27 {
			t.Fatalf("late preflight failure leaked follower selector packet msg_id=%d (total frames=%d)", frame.msgID, len(frames))
		}
	}

	// Also verify expired nativeProof is rejected cleanly.
	f.follower.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonSelectUpperPlannedWithNativeAdmission(f.leader, nativeAdmissionBody(), nil, true, nil, false, p); err != nil {
		t.Fatalf("expired native proof returned error: %v", err)
	}
	for _, frame := range splitUpperFrames(t, f.follower.conn.(*bufferConn).write.Bytes()) {
		if frame.msgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) ||
			frame.msgID == currentFatigueMsgID ||
			frame.msgID == 27 {
			t.Fatalf("expired native proof leaked follower selector packet msg_id=%d", frame.msgID)
		}
	}
}

func TestPartyNativeConnectionCommitGraceCoversFullPreflightTimeout(t *testing.T) {
	if got := partyNativeConnectionCommitGraceForMembers(2); got < partyDungeonEntryPreflightTimeout(2)+2*time.Second {
		t.Fatalf("commitGrace(2) = %v, want >= %v", got, partyDungeonEntryPreflightTimeout(2)+2*time.Second)
	}
	if got := partyNativeConnectionCommitGraceForMembers(4); got < partyDungeonEntryPreflightTimeout(4)+2*time.Second {
		t.Fatalf("commitGrace(4) = %v, want >= %v", got, partyDungeonEntryPreflightTimeout(4)+2*time.Second)
	}
	if got := partyNativeConnectionCommitGraceForMembers(16); got != 10*time.Second {
		t.Fatalf("commitGrace(16) = %v, want cap 10s", got)
	}

	f, p := nativeAdmissionProofFixture(t)
	s := f.service
	// Simulate PCQ1 completing right before the 3s query deadline: commit grace
	// must extend the commit deadline beyond the old 1s grace so preflight (>1s) succeeds.
	p.deadline = time.Now().Add(50 * time.Millisecond)
	commitGrace := partyNativeConnectionCommitGraceForMembers(len(p.peers))
	commitDeadline := time.Now().Add(commitGrace)
	p.commitDeadlineUnixNano.Store(commitDeadline.UnixNano())
	p.ready.Store(true)

	if remaining := time.Until(partyNativeProofDeadline(p)); remaining < 5*time.Second {
		t.Fatalf("partyNativeProofDeadline remaining = %v, want >= 5s (old 1s grace caused false expiry)", remaining)
	}
	if !s.partyNativeProofCurrent(p) {
		t.Fatal("partyNativeProofCurrent returned false despite extended commit grace")
	}
}

func TestLauncherDiagnosticsExposesTownReturnAndReasonCodeFields(t *testing.T) {
	s := &Service{gameSessions: make(map[uint16]*gameSession)}
	grant := s.launcherDiagnostics.issue("diag-token-p2", strings.Repeat("d", 32), time.Now())
	if grant == nil {
		t.Fatal("no launcher diagnostic grant")
	}
	session := &gameSession{
		connID:                     "game-diag-1",
		selectedCharacterID:        2358,
		characterGeneration:        1,
		launcherCharacterID:        2358,
		launcherCharacterName:      "Leader",
		sceneBootstrapTailDeferred: true,
	}
	s.gameSessions[2358] = session
	s.bindLauncherDiagnostics(session, "diag-token-p2")

	setLauncherDiagnosticReasonCode(session, "party_native_connection_not_ready")
	session.dungeon.runtime = &runtimeDungeonState{
		Dungeon:                worldmap.Dungeon{ID: 7539},
		Character:              dnfrepo.CharacterRecord{CharacterID: "2358"},
		settlementPhase:        currentDungeonSettlementPhaseCardsRevealed,
		townReturnPending:      true,
		townReturnRequestMsgID: uint16(dnfenum.CmdPacketBack2Village),
		startedAt:              time.Now(),
	}

	s.captureLauncherContext(session, false)
	fields := session.launcherDiagnostic.entry.snapshot.Fields
	if fields["town_scene_tail_deferred"].Value == nil || *fields["town_scene_tail_deferred"].Value != "true" {
		t.Fatalf("town_scene_tail_deferred = %+v, want true", fields["town_scene_tail_deferred"])
	}
	if fields["town_return_pending"].Value == nil || *fields["town_return_pending"].Value != "true" {
		t.Fatalf("town_return_pending = %+v, want true", fields["town_return_pending"])
	}
	if fields["town_return_req_msg_id"].Value == nil || *fields["town_return_req_msg_id"].Value != "132" {
		t.Fatalf("town_return_req_msg_id = %+v, want 132", fields["town_return_req_msg_id"])
	}
	if fields["reason_code"].Value == nil || *fields["reason_code"].Value != "party_native_connection_not_ready" {
		t.Fatalf("reason_code = %+v, want party_native_connection_not_ready", fields["reason_code"])
	}

	// Clearing reason code returns to "unknown"; disconnect still overrides reason_code.
	setLauncherDiagnosticReasonCode(session, "")
	session.dungeon.runtime.townReturnPending = false
	s.captureLauncherContext(session, false)
	fields = session.launcherDiagnostic.entry.snapshot.Fields
	if fields["reason_code"].Value == nil || *fields["reason_code"].Value != "unknown" {
		t.Fatalf("cleared reason_code = %+v, want unknown", fields["reason_code"])
	}
	if fields["town_return_pending"].Value == nil || *fields["town_return_pending"].Value != "false" {
		t.Fatalf("town_return_pending = %+v, want false", fields["town_return_pending"])
	}
	if fields["town_return_req_msg_id"].Value != nil {
		t.Fatalf("town_return_req_msg_id should be unavailable when not pending: %+v", fields["town_return_req_msg_id"])
	}

	s.captureLauncherContext(session, true)
	fields = session.launcherDiagnostic.entry.snapshot.Fields
	if fields["reason_code"].Value == nil || *fields["reason_code"].Value != "connection_closed_unknown" {
		t.Fatalf("disconnect reason_code = %+v, want connection_closed_unknown", fields["reason_code"])
	}
}
