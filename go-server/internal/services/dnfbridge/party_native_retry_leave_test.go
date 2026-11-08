package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The current client sends leave-party as the 13-byte legacy frame
// cmd=1/type=13 with an empty body. Reuse the same fixture encoding as the
// other actual game-stream tests, then drive the real inbound dispatcher.
// PCQ1/PCR1 in the native-retry fixture remain synthetic replays; this test
// is server control-flow coverage, not native-client acceptance evidence.
func actualLeavePartyStreamPacket(sequence uint16) dnfproto.LatestGameStreamPacket {
	raw := buildLegacyGamePacketForBridgeTest(
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		sequence,
		nil,
	)
	return dnfproto.LatestGameStreamPacket{
		Kind: dnfproto.LatestGameStreamLegacy,
		Data: raw,
	}
}

// callGameSession is the production serialization point. It runs inline for
// fixtures without an event loop and keeps the actual packet writes ordered
// against the pending native-admission worker when loops are available.
func sendActualLeavePartyStreamPacket(t *testing.T, ctx context.Context, s *Service, session *gameSession, sequence uint16) {
	t.Helper()
	packet := actualLeavePartyStreamPacket(sequence)
	if err := s.callGameSession(ctx, session, "test-op13-leave-party", func() error {
		return s.handleGameStreamPacket(session, packet)
	}); err != nil {
		t.Fatalf("op13 leave-party stream for char %d: %v", session.selectedCharacterID, err)
	}
}

func requireTownPartyManagerSingleton(t *testing.T, s *Service, label string, partyID uint16, survivorID uint16, survivorSlot byte) {
	t.Helper()
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(survivorID, 0)
	if !found || snapshot.ID != partyID || snapshot.Leader != survivorID ||
		len(snapshot.Members) != 1 || snapshot.Members[0].UserID != survivorID || snapshot.Members[0].Slot != survivorSlot {
		t.Fatalf("%s: manager singleton found=%t snapshot=%+v; want party=%d leader/member=%d slot=%d",
			label, found, snapshot, partyID, survivorID, survivorSlot)
	}
}

// A completed op13 return owns a typed op24 route and its one-shot ACK. The
// session detaches after that route is written, while the retired runtime keeps
// these completion/return markers for the deferred town-side evidence.
func requireCompletedReturnOwner(t *testing.T, label string, runtime *runtimeDungeonState) {
	t.Helper()
	if runtime == nil || runtime.Session == nil {
		t.Fatalf("%s: completed return runtime owner missing", label)
	}
	if status := runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
		t.Fatalf("%s: completed run status = %s", label, status)
	}
	if !runtime.settlementClearRewardSent || !runtime.settlementCardRewardCommitted {
		t.Fatalf("%s: committed completion reward was discarded: clear=%t card=%t",
			label, runtime.settlementClearRewardSent, runtime.settlementCardRewardCommitted)
	}
	if !runtime.townReturnPending || !runtime.townReturnOp24Sent || !runtime.townReturnAckSent ||
		runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketLeaveParty) {
		t.Fatalf("%s: completed return owner pending=%t op24=%t ack=%t request=%d; want all leave-party ownership retained",
			label, runtime.townReturnPending, runtime.townReturnOp24Sent, runtime.townReturnAckSent,
			runtime.townReturnRequestMsgID)
	}
}

func retireNativeRetryProofForTest(t *testing.T, s *Service, p *partyNativeConnectionProof) {
	t.Helper()
	// Synthetic PCR1 refusal lets the real admission worker retire promptly.
	// The assertions above already proved the old proof is not current; these
	// bytes only release the fixture worker and never claim native readiness.
	for _, peer := range p.peers {
		if err := s.handlePartyNativeConnectionReply(peer.session, nativeAdmissionReply(p, peer.session, 3)); err != nil {
			t.Fatal(err)
		}
	}
	waitNativePartyAdmissionForTest(t, s, p)
}

// TestNativeRetryPendingOp13PersonalLeaveInvalidatesProof joins the three
// previously separate behaviors: a real op13 game-stream entry, a completed
// two-person runtime with a pending native retry proof, and the final survivor
// leaving through the same entry. A PCQ1/PCR1 handshake is synthetic here.
func TestNativeRetryPendingOp13PersonalLeaveInvalidatesProof(t *testing.T) {
	for _, leaderLeavesFirst := range []bool{false, true} {
		mode := "member_leaves_first"
		if leaderLeavesFirst {
			mode = "leader_leaves_first"
		}
		t.Run(mode, func(t *testing.T) {
			f, oldLeader, oldFollower := newNativeRetryFailureFixture(t)
			service, leader, follower := f.service, f.leader, f.follower

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, peer := range []*gameSession{leader, follower} {
				if err := service.startGameSessionEvents(ctx, peer); err != nil {
					t.Fatal(err)
				}
				defer service.stopGameSessionEvents(context.Background(), peer)
			}

			if err := service.callGameSession(ctx, leader, "test-native-retry-op13", func() error {
				return service.handleDungeonEplpCommand(leader, []byte{1, 0})
			}); err != nil {
				t.Fatal(err)
			}
			p := pendingNativeRetryProof(t, service, leader)

			manager := service.runtimePartyManagerForService()
			before, found := manager.SnapshotByUser(leader.selectedCharacterID, 0)
			if !found || len(before.Members) != 2 {
				t.Fatalf("fixture party before leave: found=%t snapshot=%+v", found, before)
			}
			partyID := before.ID

			leaving, staying := follower, leader
			leavingRuntime, stayingRuntime := oldFollower, oldLeader
			leavingLabel, stayingLabel := "member", "leader"
			if leaderLeavesFirst {
				leaving, staying = leader, follower
				leavingRuntime, stayingRuntime = oldLeader, oldFollower
				leavingLabel, stayingLabel = "leader", "member"
			}
			var survivorSlot byte
			for _, member := range before.Members {
				if member.UserID == staying.selectedCharacterID {
					survivorSlot = member.Slot
				}
			}

			sendActualLeavePartyStreamPacket(t, ctx, service, leaving, 1)

			if service.partyNativeProofCurrent(p) {
				t.Fatal("pending native retry proof survived the first op13 personal leave")
			}
			if snapshot, found := manager.SnapshotByUser(leaving.selectedCharacterID, 0); found {
				t.Fatalf("departing %s remained in the runtime-party manager: %+v", leavingLabel, snapshot)
			}
			requireTownPartyManagerSingleton(t, service, "first op13 "+leavingLabel+" leave", partyID, staying.selectedCharacterID, survivorSlot)
			assertPartyProjectionEmpty(t, "departing "+leavingLabel, leaving)
			assertPartyProjectionSingleton(t, "surviving "+stayingLabel, runtimePartyStateSnapshot(staying), int(partyID), staying.selectedCharacterID, survivorSlot, true)

			staying.dungeon.mu.Lock()
			survivorRetained := staying.dungeon.runtime == stayingRuntime
			staying.dungeon.mu.Unlock()
			if !survivorRetained {
				t.Fatalf("first op13 detached the surviving %s's completed runtime", stayingLabel)
			}
			if status := stayingRuntime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
				t.Fatalf("surviving %s lost the completed run: %s", stayingLabel, status)
			}
			leaving.dungeon.mu.Lock()
			leavingDetached := leaving.dungeon.runtime == nil
			leaving.dungeon.mu.Unlock()
			if !leavingDetached {
				t.Fatalf("completed op13 left the departing %s attached to its dungeon session", leavingLabel)
			}
			requireCompletedReturnOwner(t, "departing "+leavingLabel+" completed return", leavingRuntime)

			// The surviving singleton leaves through the same actual op13 entry.
			sendActualLeavePartyStreamPacket(t, ctx, service, staying, 2)
			for _, peer := range []struct {
				label   string
				session *gameSession
			}{{"leader", leader}, {"member", follower}} {
				if snapshot, found := manager.SnapshotByUser(peer.session.selectedCharacterID, 0); found {
					t.Fatalf("%s remained in the manager after both personal leaves: %+v", peer.label, snapshot)
				}
				assertPartyProjectionEmpty(t, peer.label, peer.session)
				peer.session.dungeon.mu.Lock()
				runtime := peer.session.dungeon.runtime
				peer.session.dungeon.mu.Unlock()
				if runtime != nil {
					t.Fatalf("%s retained a dungeon session after its own completed op13 leave", peer.label)
				}
			}
			requireCompletedReturnOwner(t, "leader completed return", oldLeader)
			requireCompletedReturnOwner(t, "member completed return", oldFollower)

			if service.partyNativeProofCurrent(p) {
				t.Fatal("old native retry proof remained current after both personal leaves")
			}
			// Pretend the synthetic PCQ1 exchange had succeeded, isolating the
			// roster/currency check: the stale proof still must not admit either
			// former member into a dungeon or create a new runtime.
			p.ready.Store(true)
			for _, peer := range []*gameSession{leader, follower} {
				state := service.runtimePartyStateForSession(peer)
				deferred, err := service.deferPartyNativeConnectionAdmission(peer, p.body, state, nil, false, p)
				if deferred || err == nil {
					t.Fatalf("stale native proof reopened entry for char %d: deferred=%t err=%v", peer.selectedCharacterID, deferred, err)
				}
				peer.dungeon.mu.Lock()
				runtime := peer.dungeon.runtime
				peer.dungeon.mu.Unlock()
				if runtime != nil {
					t.Fatalf("stale native proof created a runtime for char %d", peer.selectedCharacterID)
				}
			}
			retireNativeRetryProofForTest(t, service, p)
		})
	}
}

// The ordinary-town counterpart covers the same actual op13 entry on the
// production client version without a dungeon runtime: a two-person party
// first becomes a singleton, then that singleton leaves.
func TestOrdinaryTownOfficialSingletonOp13StreamLeaveClearsParty(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	manager := h.service.runtimePartyManagerForService()
	ctx := context.Background()

	sendActualLeavePartyStreamPacket(t, ctx, h.service, h.member, 1)
	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("departed town member remained in the manager: %+v", snapshot)
	}
	requireTownPartyManagerSingleton(t, h.service, "ordinary-town member leave", h.partyID, 1001, 0)
	assertPartyProjectionEmpty(t, "departed ordinary-town member", h.member)
	assertPartyProjectionSingleton(t, "ordinary-town leader", runtimePartyStateSnapshot(h.leader), int(h.partyID), 1001, 0, true)

	sendActualLeavePartyStreamPacket(t, ctx, h.service, h.leader, 2)
	for _, id := range []uint16{1001, 1002} {
		if snapshot, found := manager.SnapshotByUser(id, 0); found {
			t.Fatalf("char %d remained in the manager after the ordinary-town singleton leave: %+v", id, snapshot)
		}
	}
	assertPartyProjectionEmpty(t, "ordinary-town leader", h.leader)
	assertPartyProjectionEmpty(t, "former ordinary-town member", h.member)
}
