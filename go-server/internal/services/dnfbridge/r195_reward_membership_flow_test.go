package dnfbridge

import (
	"testing"
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

	// Both members were present for the accepted clears. Remove 20 from the
	// current party and the live raid membership, leaving the frozen flow
	// entitlement intact.
	r195RewardParty(t, f.service, 19, 20)
	if result := f.service.runtimePartyManagerForService().LeaveCurrent(20); !result.OK {
		t.Fatalf("leave current party: %s", result.Reason)
	}
	index := runtimeRaidMemberIndex(*f.state, 20)
	if index < 0 {
		t.Fatal("fixture member 20 missing from live raid")
	}
	f.state.Members = append(f.state.Members[:index], f.state.Members[index+1:]...)
	if runtimeRaidMemberIndex(*f.state, 20) >= 0 || len(flow.Recipients) != 2 {
		t.Fatal("departure changed the frozen recipient snapshot")
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
