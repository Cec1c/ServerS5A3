package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestR195RealOp14KickFiltersNextRewardProjectionAndKeepsFrozenGrants
// uses the real op14 WalkoutPartyMember
// handler, then explicitly asks for the same reward projection used by the
// production reward queue.
//
// A successful op14 handler proves only that the central party mutation and
// its party broadcasts succeeded. It is not evidence that a native client
// removed an already-rendered reward row. The assertion after the handler
// records that op14 sends no NEW_600 refresh by itself; the later filtered
// list comes from the explicit projection call below.
func TestR195RealOp14KickFiltersNextRewardProjectionAndKeepsFrozenGrants(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	peer, ok := f.service.onlineGameSession(20)
	if !ok {
		t.Fatal("fixture peer 20 is not online")
	}

	// Rebuild the ordinary party with the generations carried by the bound
	// sessions. sessions[0] is the leader, so the manager assigns 19 slot 0
	// and the joined peer 20 slot 1.
	a02RewardPartyWithSessionGenerations(t, f.service, session, peer)
	manager := f.service.runtimePartyManagerForService()
	beforeKick, found := manager.SnapshotByUser(19, 0)
	if !found || beforeKick.Leader != 19 {
		t.Fatalf("central party leader=%d found=%t, want 19", beforeKick.Leader, found)
	}
	var leaderSlot, peerSlot byte
	leaderFound, peerFound := false, false
	for _, member := range beforeKick.Members {
		switch member.UserID {
		case 19:
			leaderSlot, leaderFound = member.Slot, true
		case 20:
			peerSlot, peerFound = member.Slot, true
		}
	}
	if !leaderFound || !peerFound || leaderSlot != 0 || peerSlot != 1 {
		t.Fatalf("central party slots: 19=%d/%t 20=%d/%t, want 19=0 20=1", leaderSlot, leaderFound, peerSlot, peerFound)
	}

	flow := f.state.Battle.RewardFlow[0]
	clears20 := currentAntonPhaseClearCount(f.state.Battle, 20)
	group20 := f.state.Battle.MemberGroups[20]
	if flow == nil || len(flow.Recipients) != 2 ||
		flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("fixture recipients = %+v, want frozen [19,20]", flow)
	}

	// Establish the ordinary terminal result, then build the pre-kick party
	// card through the actual op659 path. The initial wire must contain both
	// recipients so the kick is exercised against an existing full roster.
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	// op659 emits both kind1 and the kind0 cover list. The automatic-flow
	// summarizer intentionally rejects kind0, so inspect this manual exchange
	// separately and require one of each with the same exact owners.
	initialKinds := map[byte]int{}
	initialScreens := 0
	stream := f.conn.write.Bytes()
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case 587:
			if len(packet.Body) == 2 && packet.Body[0] == 4 && packet.Body[1] == 0 {
				initialScreens++
			}
		case 600:
			if len(packet.Body) < 2 || (packet.Body[0] != 0 && packet.Body[0] != 1) {
				t.Fatalf("unexpected initial reward body=%x", packet.Body)
			}
			initialKinds[packet.Body[0]]++
			if !r195WantOwners(parsePhase0CardOwners(t, packet.Body), 19, 20) {
				t.Fatalf("pre-kick kind%d must contain 19 and 20: %x", packet.Body[0], packet.Body)
			}
		}
	}
	if initialScreens != 1 || initialKinds[0] != 1 || initialKinds[1] != 1 {
		t.Fatalf("pre-kick screen=%d kinds=%v, want one screen and one list of each kind0/1", initialScreens, initialKinds)
	}

	// Current EXE op14 body is exactly one u8 slot. The leader is 19 and the
	// fixture's joined member 20 occupies slot 1.
	kickBody := []byte{peerSlot}
	if len(kickBody) != 1 || kickBody[0] != 1 {
		t.Fatalf("op14 payload=%x, want the single byte 01", kickBody)
	}
	f.conn.write.Reset()
	handled, err := f.service.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		kickBody,
	)
	if err != nil || !handled {
		t.Fatalf("op14 walkout party member: handled=%t err=%v", handled, err)
	}
	if _, still := manager.SnapshotByUser(20, 0); still {
		t.Fatal("op14 did not remove 20 from the central party manager")
	}
	remaining, found := manager.SnapshotByUser(19, 0)
	if !found || remaining.Leader != 19 {
		t.Fatalf("op14 removed leader or party: leader=%d found=%t", remaining.Leader, found)
	}
	if runtimeRaidMemberIndex(*f.state, 20) < 0 {
		t.Fatal("ordinary-party kick incorrectly removed 20 from the raid")
	}
	if currentAntonPhaseClearCount(f.state.Battle, 20) != clears20 || f.state.Battle.MemberGroups[20] != group20 {
		t.Fatal("ordinary-party kick changed frozen clears or group")
	}
	if len(flow.Recipients) != 2 || flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("op14 changed frozen recipients: %+v", flow.Recipients)
	}

	// op14's success means the party mutation happened. It does not prove that
	// the native client removed a row already on screen. In this source state
	// the handler emits no reward-card projection at all, so assert that gap
	// separately from the next authoritative projection below.
	immediate := r195SummarizeRewardWire(t, f)
	if immediate.screens != 0 || immediate.kind1Lists != 0 || immediate.kind3Lists != 0 ||
		len(immediate.kind1Owner) != 0 || len(immediate.kind3Owner) != 0 {
		t.Fatalf("op14 unexpectedly emitted a reward refresh: %+v", immediate)
	}

	// Request the production reward projection explicitly. This proves the
	// server's next party card is filtered; it does not prove native old-row
	// deletion or that an open client panel refreshed without a request/clock.
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	afterKick := r195SummarizeRewardWire(t, f)
	if afterKick.screens != 0 || afterKick.kind1Lists != 1 || afterKick.kind3Lists != 0 ||
		!r195WantOwners(afterKick.kind1Owner, 19) {
		t.Fatalf("post-kick party projection = %+v, want only current member 19", afterKick)
	}

	// The frozen Recipients still own both pools. Grant the ordinary party
	// pool, then the squad pool, using the existing deterministic roll helper.
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 ||
		len(flow.Party) != 2 || len(flow.Squad) != 0 {
		t.Fatalf("automatic party stage: errs=%v party=%d squad=%d", errs, len(flow.Party), len(flow.Squad))
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(currentAntonMovieWindow(flow)), zeroAntonRoll); len(errs) != 0 ||
		flow.SquadShownAt.IsZero() {
		t.Fatalf("automatic squad stage: shown=%t errs=%v", !flow.SquadShownAt.IsZero(), errs)
	}
	for _, id := range []uint16{19, 20} {
		party := flow.Party[id]
		squad := flow.Squad[id]
		if party == nil || !party.Committed || squad == nil || !squad.Committed {
			t.Fatalf("recipient %d lost a committed pool: party=%+v squad=%+v", id, party, squad)
		}
	}
	for _, id := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, id, 10304529); got != 30 {
			t.Fatalf("recipient %s party-pool item count=%d, want 30", id, got)
		}
		if got := antonGrantedItemCount(t, f, id, 10093974); got != 19 {
			t.Fatalf("recipient %s squad-pool item count=%d, want 19", id, got)
		}
	}

	// The squad card intentionally follows the frozen Recipients rather than
	// the current ordinary party, while the ordinary party pool was already
	// filtered above. The final projection must not duplicate either grant.
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	squad := r195SummarizeRewardWire(t, f)
	if squad.screens != 0 || squad.kind1Lists != 0 || squad.kind3Lists != 1 ||
		!r195WantOwners(squad.kind3Owner, 19, 20) {
		t.Fatalf("frozen squad projection = %+v, want 19 and 20", squad)
	}
	for _, id := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, id, 10304529); got != 30 {
			t.Fatalf("recipient %s party-pool count changed after projection: %d", id, got)
		}
		if got := antonGrantedItemCount(t, f, id, 10093974); got != 19 {
			t.Fatalf("recipient %s squad-pool count changed after projection: %d", id, got)
		}
	}
	if len(flow.Recipients) != 2 || flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("final frozen recipients changed: %+v", flow.Recipients)
	}
}
