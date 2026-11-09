package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestWeddingPreparationBoxRealPVFCanYieldRequiredDoll(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired runtime PVF")
	}
	source, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, 10002616, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source=%s type=%s groups=%+v", definition.Source.PVFPath, definition.StackableType, definition.Random.Groups)
	if len(definition.Random.Groups) != 1 || definition.Random.Groups[0].DrawCount != 1 {
		t.Fatalf("unexpected preparation box draws: %+v", definition.Random)
	}
	var cumulative, dollRoll int64
	found := false
	for _, entry := range definition.Random.Groups[0].Entries {
		if entry.ItemID == 10002610 && entry.Weight > 0 && entry.Count >= 1 {
			dollRoll, found = cumulative, true
		}
		cumulative += entry.Weight
	}
	if !found {
		t.Fatal("preparation box cannot yield quest 20738's doll")
	}
	amounts, options, err := resolveCurrentBoosterRewardAmounts(definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(limit int64) (int64, error) {
		if limit != cumulative {
			t.Fatalf("weight total %d, expected %d", limit, cumulative)
		}
		return dollRoll, nil
	})
	if err != nil || len(amounts) != 1 || amounts[10002610] < 1 {
		t.Fatalf("doll outcome: %v %v", amounts, err)
	}
	rewards, err := resolveCurrentBoosterRewards(catalog, amounts, options, r.ItemStack{ItemID: 10002616, Count: 1}, time.Now().UTC())
	if err != nil || len(rewards) != 1 || rewards[0].Definition.ItemID != 10002610 || rewards[0].Placement != currentBoosterRewardMain {
		t.Fatalf("doll grant: %+v %v", rewards, err)
	}
	if definition.LotteryGoldCost != 0 || definition.Random.MaterialItemID != 0 {
		t.Fatalf("preparation box unexpectedly charges an opening cost: %+v", definition)
	}
	// Use the ordinary native booster route with the full, unmodified PVF
	// pool. The deterministic roll above proves the doll is reachable; this
	// actual transaction may yield either advertised outcome.
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:65": {ItemID: 10002616, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := currentBoosterTestService(catalog, repos)
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	handled, err := currentGameplayModules.DispatchUpper(s, p, 160, 1, []byte{65, 0})
	if err != nil || !handled {
		t.Fatalf("native preparation box route: handled=%v err=%v", handled, err)
	}
	if ack := guildReply(t, p, 160); len(ack) == 0 || ack[0] != 1 {
		t.Fatalf("native box ACK %x", ack)
	}
	inventory, foundInventory, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !foundInventory {
		t.Fatalf("box inventory: %v %v", foundInventory, err)
	}
	var rewardCount int64
	for _, stack := range inventory.Slots {
		if stack.ItemID != 10002610 && stack.ItemID != 10002617 {
			t.Fatalf("unexpected preparation reward: %+v", stack)
		}
		rewardCount += stack.Count
	}
	if rewardCount != 1 {
		t.Fatalf("box consumed/granted incorrectly: %+v", inventory.Slots)
	}
	// The second preparation quest awards ordinary coupon stacks. Their
	// actual PVF action must remain usable by the native 1037 marriage route.
	loveService, actor, loveRepos := weddingLovePointFixture(t, 0)
	loveService.pvfItemCatalog = catalog
	loveService.wedding.rules, err = wedding.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	dispatchWeddingLovePoint(t, loveService, actor, 65, 500)
	if ack := guildReply(t, actor, 1037); len(ack) != 5 || ack[0] != 1 || ack[1] != 0 {
		t.Fatalf("actual PVF preparation coupon ACK %x", ack)
	}
	points, count := lovePointStoredState(t, loveRepos)
	if points != 1 || count != 1 {
		t.Fatalf("actual PVF preparation coupon: points=%d source=%d", points, count)
	}
}
