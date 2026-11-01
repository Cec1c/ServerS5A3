package dungeon

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestActivityPickupAtomicFullBagFailureReplayAndRarities(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1"})
	g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1"})
	owner, _ := NewOwner(g)
	for i, rarity := range []int64{3, 4, 5} {
		metric := map[int64]string{3: repo.ActivityUnique, 4: repo.ActivityEpic, 5: repo.ActivityLegendary}[rarity]
		e := repo.ActivityEvent{ID: metric, AccountID: "a", CharacterID: "1", Metric: metric, Amount: 1, OccurredAt: time.Now(), DungeonID: 310, ItemID: uint32(100 + i), Rarity: rarity, Source: "dungeon_pickup"}
		cmd := PickupItemCommand{CharacterID: "1", Activity: &e, UpdatedAt: e.OccurredAt, Placement: PickupItemPlacement{Definition: PickupItemDefinition{ItemID: e.ItemID, Kind: PickupItemEquipment, SlotStart: 1, SlotEnd: 3}, Amount: 1, BuildNew: func(int16) (repo.ItemStack, error) { return repo.ItemStack{ItemID: int64(e.ItemID), Count: 1}, nil }}}
		if _, err := owner.GrantPickupItem(ctx, cmd); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.GrantPickupItem(ctx, cmd); !errors.Is(err, repo.ErrActivityConflict) {
			t.Fatal("重放没有阻止重复发奖", err)
		}
	}
	e := repo.ActivityEvent{ID: "full", AccountID: "a", CharacterID: "1", Metric: repo.ActivityEpic, Amount: 1, OccurredAt: time.Now(), DungeonID: 310, ItemID: 999, Rarity: 4, Source: "dungeon_pickup"}
	cmd := PickupItemCommand{CharacterID: "1", Activity: &e, UpdatedAt: e.OccurredAt, Placement: PickupItemPlacement{Definition: PickupItemDefinition{ItemID: e.ItemID, Kind: PickupItemEquipment, SlotStart: 1, SlotEnd: 3}, Amount: 1, BuildNew: func(int16) (repo.ItemStack, error) { return repo.ItemStack{ItemID: 999, Count: 1}, nil }}}
	if _, err := owner.GrantPickupItem(ctx, cmd); !errors.Is(err, ErrPickupInventoryFull) {
		t.Fatal(err)
	}
	cmd.Placement.Definition.SlotEnd = 4
	cmd.Finalize = func(int16, repo.ItemStack) (repo.ItemStack, error) {
		return repo.ItemStack{}, errors.New("实例生成失败")
	}
	if _, err := owner.GrantPickupItem(ctx, cmd); err == nil {
		t.Fatal("预期失败")
	}
	summary, _ := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if summary.Metrics[repo.ActivityEpic] != 1 || summary.Metrics[repo.ActivityUnique] != 1 || summary.Metrics[repo.ActivityLegendary] != 1 {
		t.Fatal(summary)
	}
	bag, _, _ := g.Inventory.Load(ctx, "1")
	if len(bag.Slots) != 3 {
		t.Fatal("失败拾取修改了库存")
	}
}
