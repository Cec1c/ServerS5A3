package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestR195FiendWarPortPreservesOrdinaryCapacityAndTown48(t *testing.T) {
	for _, route := range []byte{0, 1} {
		s := runtimeRaidState{RouteOrRaidType: route}
		if currentRaidMemberMaximum(s) != 20 || currentRaidGroupMaximum(s) != runtimeRaidMaxGroups || currentRaidGroupCapacity(s) != runtimeRaidGroupSize {
			t.Fatal("ordinary raid inherited FiendWar seat rules", route)
		}
	}
	if currentRegisteredChannelTown(48) || !currentRegisteredChannelTown(98) {
		t.Fatal("FiendWar town registration changed ordinary town48")
	}
}

func TestRealPVFCurrentFiendWarPortPreservesOrdinaryDailyProjection(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	p := peers[0]
	for _, channel := range []byte{23, 32} {
		p.channel.Type = channel
		p.conn.(*bufferConn).write.Reset()
		if err := f.service.sendCurrentRaidDailyAvailability(p, "ordinary boundary control"); err != nil {
			t.Fatal(err)
		}
		if p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("ordinary channel gained daily snapshot", channel)
		}
	}
	// The same authenticated current connection does receive the real three-row
	// contract in FiendWar. This is projection only; it does not debit other modes.
	p.channel.Type = 42
	if err := f.service.sendCurrentRaidDailyAvailability(p, "FiendWar boundary control"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 1303 || len(packet.Body) != 12 || packet.Body[0] != 1 || packet.Body[4] != 2 || packet.Body[8] != 3 {
		t.Fatal("FiendWar daily snapshot missing or misaligned", packet)
	}
}

func TestRealPVFCurrentFiendWarPortPreservesLukeStartWithoutNewWaitingOrDailyDebit(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	s, p := f.service, peers[0]
	p.channel.ID, p.channel.Type = 32, 32
	f.state.RouteOrRaidType, f.state.CurrentUIFlags, f.state.TailFlag = 1, [3]byte{}, 0
	f.state.Started, f.state.Battle = false, nil
	ctx := context.Background()
	rule, err := s.currentRaidAdmissionRule(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{
		2903: {Status: "completed"}, 12830: {Status: "completed"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{
		"0:50": {ItemID: int64(rule.ItemID), Count: 2 * rule.Count}, "0:51": {ItemID: 10159142, Count: 40},
	}}); err != nil {
		t.Fatal(err)
	}
	daily, err := s.currentRaidDailyRule(2)
	if err != nil || daily == nil {
		t.Fatal("real Luke daily configuration unavailable", err)
	}
	before, beforeFound, err := f.repos.Settings.Load(ctx, daily.Scope)
	if err != nil {
		t.Fatal(err)
	}
	// No waiting-area presence is created. The public Luke start requires its
	// original prerequisites and ticket, not the new FiendWar waiting scope.
	state, ok, err := s.startRuntimeRaidWithEntryCost(p)
	if err != nil || !ok || state.Battle == nil || state.Battle.Mode != 2 {
		t.Fatal("ordinary Luke start changed", ok, err)
	}
	after, afterFound, err := f.repos.Settings.Load(ctx, daily.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if beforeFound != afterFound || !reflect.DeepEqual(before, after) {
		t.Fatal("ordinary Luke start debited a new daily ledger")
	}
	bag, _, err := f.repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if bag.Slots["0:50"].Count != rule.Count || bag.Slots["0:51"].Count != 40 {
		t.Fatal("ordinary ticket/holding transaction changed", bag)
	}
}

func TestRealPVFCurrentFiendWarPortPreservesAntonNextPhaseWithoutNewWaitingGate(t *testing.T) {
	f, p, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	before, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	state, ok, err := f.service.startRuntimeRaidWithEntryCost(p)
	if err != nil || !ok || state.Battle == nil || state.Battle.Phase != 1 {
		t.Fatal("ordinary next phase gained FiendWar waiting gate", ok, err)
	}
	after, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ordinary next phase changed inventory")
	}
}
