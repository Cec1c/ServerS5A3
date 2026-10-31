package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

type r195RewardWireSummary struct {
	screens    int
	kind1Lists int
	kind3Lists int
	kind1Owner []uint16
	kind3Owner []uint16
}

func r195SummarizeRewardWire(t *testing.T, f *raidStartGuardFixture) r195RewardWireSummary {
	t.Helper()
	var got r195RewardWireSummary
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
				got.screens++
			}
		case 600:
			if len(packet.Body) == 0 {
				t.Fatal("empty 600 body")
			}
			switch packet.Body[0] {
			case 1:
				got.kind1Lists++
				got.kind1Owner = append(got.kind1Owner, parsePhase0CardOwners(t, packet.Body)...)
			case 3:
				got.kind3Lists++
				got.kind3Owner = append(got.kind3Owner, parsePhase0CardOwners(t, packet.Body)...)
			default:
				t.Fatalf("unexpected reward kind %d body=%x", packet.Body[0], packet.Body)
			}
		}
	}
	return got
}

func r195WantOwners(owners []uint16, want ...uint16) bool {
	if len(owners) != len(want) {
		return false
	}
	for i := range want {
		if owners[i] != want[i] {
			return false
		}
	}
	return true
}

func TestR195QualifiedDepartedMemberLeavesPartyCardAndKeepsSquadReward(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	minimum := flow.Rewards.MinimumClears
	if minimum == 0 || currentAntonPhaseClearCount(f.state.Battle, 19) < minimum || currentAntonPhaseClearCount(f.state.Battle, 20) < minimum {
		t.Fatal("fixture members do not satisfy the stage minimum before departure")
	}
	if len(flow.Recipients) != 2 {
		t.Fatalf("fixture recipients=%d, want 2", len(flow.Recipients))
	}
	if flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("fixture recipients changed: %+v", flow.Recipients)
	}

	// Both members were present for the accepted clears. Drive the real
	// current-client commands: op13 removes 20 from the ordinary party while
	// leaving it in the raid; op665 then removes 20 from the raid. The frozen
	// flow entitlement must survive both.
	peer, ok := f.service.onlineGameSession(20)
	if !ok {
		t.Fatal("fixture peer 20 is not online")
	}
	a02RewardPartyWithSessionGenerations(t, f.service, session, peer)
	clears20 := currentAntonPhaseClearCount(f.state.Battle, 20)
	group20 := f.state.Battle.MemberGroups[20]
	handled, err := f.service.handleAlignedGameCommand(
		peer,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil || !handled {
		t.Fatalf("op13 leave party: handled=%t err=%v", handled, err)
	}
	manager := f.service.runtimePartyManagerForService()
	if _, still := manager.SnapshotByUser(20, 0); still {
		t.Fatal("op13 did not remove 20 from the central party manager")
	}
	if _, still := manager.SnapshotByUser(19, 0); !still {
		t.Fatal("op13 removed 19 from the central party manager")
	}
	if runtimeRaidMemberIndex(*f.state, 20) < 0 {
		t.Fatal("op13 removed 20 from the raid")
	}
	// handleOnlineLeaveRaid has no phase/started gate: it decodes the u16
	// member key and removes that member. op665 is therefore valid during
	// this frozen phase-0 reward flow.
	handled, err = f.service.handleAlignedGameCommand(
		peer,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveRaid),
		[]byte{20, 0},
	)
	if err != nil || !handled {
		t.Fatalf("op665 leave raid: handled=%t err=%v", handled, err)
	}
	if runtimeRaidMemberIndex(*f.state, 20) >= 0 || len(flow.Recipients) != 2 {
		t.Fatal("departure changed the frozen recipient snapshot")
	}
	if flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("frozen recipients changed: %+v", flow.Recipients)
	}
	if got := currentAntonPhaseClearCount(f.state.Battle, 20); got != clears20 {
		t.Fatalf("raid leave changed 20's clear count: %d -> %d", clears20, got)
	}
	if got := f.state.Battle.MemberGroups[20]; got != group20 {
		t.Fatalf("raid leave changed 20's frozen group: %d -> %d", group20, got)
	}

	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 2 || len(flow.Squad) != 0 {
		t.Fatalf("automatic party stage: errs=%v party=%d squad=%d", errs, len(flow.Party), len(flow.Squad))
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	party := r195SummarizeRewardWire(t, f)
	if party.screens != 1 || party.kind1Lists != 1 || party.kind3Lists != 0 || !r195WantOwners(party.kind1Owner, 19) {
		t.Fatalf("departed member leaked into current party card: %+v", party)
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(currentAntonMovieWindow(flow)), zeroAntonRoll); len(errs) != 0 || flow.SquadShownAt.IsZero() {
		t.Fatalf("squad stage did not complete: shown=%t errs=%v", !flow.SquadShownAt.IsZero(), errs)
	}
	for _, id := range []uint16{19, 20} {
		card := flow.Squad[id]
		if card == nil || !card.Committed {
			t.Fatalf("departed member %d lost frozen squad grant", id)
		}
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	squad := r195SummarizeRewardWire(t, f)
	if squad.screens != 0 || squad.kind1Lists != 0 || squad.kind3Lists != 1 || !r195WantOwners(squad.kind3Owner, 19, 20) {
		t.Fatalf("squad card did not retain both frozen recipients without restart: %+v", squad)
	}
	for _, id := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, id, 10093974); got != 19 {
			t.Fatalf("departed member %s squad reward=%d, want 19", id, got)
		}
	}
}

func TestR195AutomaticPartyToSquadEmitsKind3WithoutState4Replay(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 2 || len(flow.Squad) != 0 {
		t.Fatalf("automatic party stage: errs=%v party=%d squad=%d", errs, len(flow.Party), len(flow.Squad))
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	party := r195SummarizeRewardWire(t, f)
	if party.screens != 1 || party.kind1Lists != 1 || party.kind3Lists != 0 || !r195WantOwners(party.kind1Owner, 19, 20) {
		t.Fatalf("automatic party presentation: %+v", party)
	}
	if !session.raidProjection.rewardScreenSent || session.raidProjection.rewardScreenIdentity != (currentRaidProjectionIdentity{InstanceID: f.state.Battle.InstanceID, Phase: 0, CharID: 19, Generation: session.characterGeneration}) {
		t.Fatal("party screen cache was not established for the connection")
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(currentAntonMovieWindow(flow)), zeroAntonRoll); len(errs) != 0 || flow.SquadShownAt.IsZero() {
		t.Fatalf("automatic squad completion: shown=%t errs=%v", !flow.SquadShownAt.IsZero(), errs)
	}
	for _, id := range []uint16{19, 20} {
		if card := flow.Squad[id]; card == nil || !card.Committed {
			t.Fatalf("squad card %d not committed", id)
		}
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	squad := r195SummarizeRewardWire(t, f)
	if squad.screens != 0 || squad.kind1Lists != 0 || squad.kind3Lists != 1 || !r195WantOwners(squad.kind3Owner, 19, 20) {
		t.Fatalf("automatic party to squad swallowed/replayed presentation: %+v", squad)
	}

	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	duplicate := r195SummarizeRewardWire(t, f)
	if duplicate.screens != 0 || duplicate.kind1Lists != 0 || duplicate.kind3Lists != 0 {
		t.Fatalf("unchanged squad presentation replayed: %+v", duplicate)
	}
}
