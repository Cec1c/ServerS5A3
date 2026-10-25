package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func raidWaitingInventories(t *testing.T, f *raidStartGuardFixture) map[uint16]dnfrepo.InventoryRecord {
	t.Helper()
	bags := make(map[uint16]dnfrepo.InventoryRecord)
	for _, member := range f.state.Members {
		bag, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(member.CharID)))
		if !found || err != nil {
			t.Fatal("读取隔离背包失败", found, err)
		}
		bags[member.CharID] = bag
	}
	return bags
}

func partyOnlyLukeSettlementFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, *gameSession, *currentAntonRewardFlow) {
	t.Helper()
	f, leader, at := normalLukeFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, leader, id, at)
			at = at.Add(time.Second)
		}
	}
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || !f.state.Battle.PhaseCompleted || flow.squadRequired() {
		t.Fatal("real Luke party-only settlement fixture")
	}
	if len(flow.Recipients) != 2 || flow.Recipients[0].Group == flow.Recipients[1].Group {
		t.Fatal("real Luke settlement fixture must contain two reward groups")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(flow.Squad) != 0 || !currentLukePhase0PartyOnlyRewardSettled(flow) {
		t.Fatal("party-only cards did not settle")
	}
	return f, leader, peer, flow
}

func TestRealPVFR169LukePartyOnlySettlementCanStartSecondPhase(t *testing.T) {
	f, leader, _, flow := partyOnlyLukeSettlementFixture(t)
	if flow.FinishedAt.IsZero() || flow.BreakUntil.IsZero() {
		t.Fatal("settled party-only flow retained the phantom squad display wait")
	}
	if got := currentRuntimeRaidMetadata(*f.state); got.State != 5 || got.Phase != 0 {
		t.Fatalf("settled party-only metadata = %d/%d, want waiting state 5/0", got.State, got.Phase)
	}
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	waitingBags := raidWaitingInventories(t, f)

	state, ok, err := f.service.startRuntimeRaidWithEntryCost(leader)
	if !ok || err != nil || state.Battle == nil || state.Battle.Phase != 1 || state.Battle.PhaseCompleted {
		t.Fatal("settled party-only Luke flow could not start phase two", ok, err, state.Battle)
	}
	if got := currentRuntimeRaidMetadata(*f.state); got.State != 2 || got.Phase != 1 {
		t.Fatalf("second phase metadata = %d/%d, want active state 2/1", got.State, got.Phase)
	}
	if !reflect.DeepEqual(waitingBags, raidWaitingInventories(t, f)) || len(flow.Squad) != 0 || len(flow.Party) != len(flow.Recipients) {
		t.Fatal("second phase start charged or rerolled first-phase rewards")
	}
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); ok || err != nil {
		t.Fatal("duplicate second-phase start accepted", ok, err)
	}
}

func TestRealPVFR169LukePartyOnlySettlementStillNeedsEveryRewardGroup(t *testing.T) {
	f, leader, _, flow := partyOnlyLukeSettlementFixture(t)
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	f.service.raidMu.Lock()
	flow.Party[20].Committed = false
	// Reconstruct a genuinely unfinished flow: committed receipts never
	// revert in production, and its terminal anchors cannot survive here.
	flow.FinishedAt, flow.BreakUntil = time.Time{}, time.Time{}
	before := cloneRuntimeRaidState(*f.state)
	f.service.raidMu.Unlock()
	if got := currentRuntimeRaidMetadata(*f.state); got.State != 3 {
		t.Fatalf("unsettled other group metadata state = %d, want settlement state 3", got.State)
	}
	state, ok, err := f.service.startRuntimeRaidWithEntryCost(leader)
	if ok || err != nil || state.Battle != nil || !reflect.DeepEqual(before, cloneRuntimeRaidState(*f.state)) || f.state.Battle.Phase != 0 {
		t.Fatal("other reward group bypassed settlement gate", ok, err)
	}
	f.service.raidMu.Lock()
	flow.Party[20].Committed = true
	f.service.raidMu.Unlock()
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); !ok || err != nil || f.state.Battle.Phase != 1 {
		t.Fatal("all reward groups settled but next phase stayed blocked", ok, err)
	}
}

func TestRealPVFR169LukeIncompleteFirstPhaseStillCannotStartSecond(t *testing.T) {
	f, leader, _ := normalLukeFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	before := cloneRuntimeRaidState(*f.state)
	if state, ok, err := f.service.startRuntimeRaidWithEntryCost(leader); ok || err != nil || state.Battle != nil || !reflect.DeepEqual(before, cloneRuntimeRaidState(*f.state)) || f.state.Battle.Phase != 0 {
		t.Fatal("incomplete Luke first phase started second phase", ok, err)
	}
}
