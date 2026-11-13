package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRaidFateWeeklyProgressFromRealRewardPlanAcrossCharacters(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	account := "weekly-raid-fate"
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: account}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"31", "32"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: account, CharacterID: id}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	plan := fateRewardBridgePlan(t, 100)
	for i, id := range []string{"31", "32", "31"} {
		cmd := raid.RewardCommand{AccountID: account, CharacterID: id, InstanceID: fmt.Sprintf("%032x", i+1), Mode: 1, Kind: 1, MinimumClears: 1, OwnedClears: 1, Fingerprint: plan.Fingerprint, CompletedAt: fateRewardBridgeTime()}
		for replay := 0; replay < 2; replay++ {
			result, err := grantCurrentAntonRewardPlan(ctx, g, nil, plan, cmd)
			if err != nil || result.Replayed != (replay == 1) {
				t.Fatalf("grant %d replay %d: %+v %v", i, replay, result, err)
			}
		}
		if i == 0 {
			// Exact pre-fix persisted shape: Fate ledger receipt, no event counter.
			ch, _, _ := g.Character.Load(ctx, id)
			delete(ch.Stats, fmt.Sprintf("%s%s:43:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(cmd.CompletedAt)))
			delete(ch.Stats, fmt.Sprintf("raid_reward_v1_1_%s_0_1_bp_fate_v1", cmd.InstanceID))
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		chars, err := g.Character.ListByAccount(ctx, account, 100)
		if err != nil {
			t.Fatal(err)
		}
		var actual int64
		policy := bp.Policy{ID: 1, Start: cmd.CompletedAt.Add(-24 * time.Hour), End: cmd.CompletedAt.Add(24 * time.Hour)}
		counters, err := loadBattlePassCounters(ctx, g, account, policy, cmd.CompletedAt)
		if err != nil {
			t.Fatal(err)
		}
		for _, counter := range counters {
			if counter.Kind == 43 && counter.Param == -1 {
				actual += counter.Amount
			}
		}
		if actual != int64((i+1)*100) {
			t.Fatalf("weekly raid Fate=%d want=%d", actual, (i+1)*100)
		}
		after, err := g.Character.ListByAccount(ctx, account, 100)
		if err != nil || !reflect.DeepEqual(chars, after) {
			t.Fatal("query rewrote historical reward/counter state")
		}
	}
}
