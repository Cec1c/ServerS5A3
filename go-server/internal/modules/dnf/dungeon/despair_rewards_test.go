package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func despairRewardFixture(t *testing.T) (dnfrepo.Group, SettlementCommand) {
	t.Helper()
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.DespairClearedFloor = 1
	cmd.DespairRewards = &DespairRewards{Items: []DespairRewardItem{
		{CardItemReward: CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 100, Count: 1, Extra: map[string]string{"quality_seed": "123", "seal_flag": "1"}}, SlotStart: 9, SlotEnd: 40}},
		{CardItemReward: CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 3314, Count: 1}, Stackable: true, SlotStart: 121, SlotEnd: 152}},
		{ListType: 1, CardItemReward: CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "avatar", "ext_data0": "0", "amount_or_count": "0"}}}},
	}}
	return g, cmd
}

func TestDespairRewardsCommitAndReplayOriginalItemsWithFloor(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairRewardFixture(t)
	owner, _ := NewOwner(g)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || len(first.DespairItems) != 3 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if first.Character.Stats[DespairHighestClearedStatKey] != 1 || first.Inventory.Slots["0:9"].ItemID != 100 || first.Inventory.Slots["0:121"].ItemID != 3314 || first.Inventory.Slots["1:0"].ItemID != 200 {
		t.Fatal("partial or wrong-list reward")
	}
	if first.Inventory.Slots["0:9"].Extra["quality_seed"] != "123" || first.Inventory.Slots["0:9"].Extra["seal_flag"] != "1" {
		t.Fatal("instance identity lost")
	}
	cmd.DespairRewards.Items[0].Stack.ItemID = 999
	replay, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.DespairItems, first.DespairItems) || !reflect.DeepEqual(replay.Inventory.Slots, first.Inventory.Slots) {
		t.Fatalf("receipt replay changed award err=%v", err)
	}
	// A later ordinary clear must not try to read the previous tower receipt.
	cmd.CompletionKey = "ordinary-after-tower"
	cmd.DespairClearedFloor = 0
	cmd.DespairRewards = nil
	if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
}

func TestDespairRewardsFullSecondPageRollsBackWholeSettlement(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairRewardFixture(t)
	inventory, _, _ := g.Inventory.Load(ctx, "c")
	inventory.Slots = map[string]dnfrepo.ItemStack{}
	for i := 121; i <= 152; i++ {
		inventory.Slots[mainSlotKey(uint16(i))] = dnfrepo.ItemStack{ItemID: 444, Count: 1}
	}
	if err := g.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(g)
	if _, err := owner.CommitSettlement(ctx, cmd); !errors.Is(err, ErrInventoryFull) {
		t.Fatalf("err=%v", err)
	}
	after, _, _ := g.Inventory.Load(ctx, "c")
	c, _, _ := g.Character.Load(ctx, "c")
	if !reflect.DeepEqual(after.Slots, inventory.Slots) || c.Level != 1 || c.Stats["exp"] != 90 || c.Stats[DespairHighestClearedStatKey] != 0 {
		t.Fatal("failed grant left partial award/floor/exp")
	}
}

type despairFailInventory struct{ dnfrepo.InventoryRepository }

func (r despairFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("despair inventory save failure")
}

type despairFailUOW struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u despairFailUOW) WithinCharacterSettlement(ctx context.Context, id string, f func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error { tx.Inventory = despairFailInventory{tx.Inventory}; return f(tx) })
}

func TestDespairRewardsSaveFailureRollsBackFloorAndReceipt(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairRewardFixture(t)
	g.CharacterSettlement = despairFailUOW{g.CharacterSettlement}
	owner, _ := NewOwner(g)
	if _, err := owner.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("save failure accepted")
	}
	c, _, _ := g.Character.Load(ctx, "c")
	bag, _, _ := g.Inventory.Load(ctx, "c")
	if c.Level != 1 || c.Stats["exp"] != 90 || c.Stats[DespairHighestClearedStatKey] != 0 || len(bag.Slots) != 0 || c.Stats[despairRewardReceiptPrefix+"version"] != 0 {
		t.Fatal("failed save partially committed")
	}
}

func TestDespairRewardsDoNotOverflowUnopenedCapacity(t *testing.T) {
	ctx := context.Background()
	g, cmd := despairRewardFixture(t)
	cmd.DespairRewards.Items = cmd.DespairRewards.Items[:1]
	bag, _, _ := g.Inventory.Load(ctx, "c")
	bag.Slots = map[string]dnfrepo.ItemStack{}
	for slot := 9; slot <= 40; slot++ {
		bag.Slots[mainSlotKey(uint16(slot))] = dnfrepo.ItemStack{ItemID: 444, Count: 1}
	}
	if err := g.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	cmd.DespairRewards.Items[0].SlotEnd = 64
	owner, _ := NewOwner(g)
	if _, err := owner.CommitSettlement(ctx, cmd); !errors.Is(err, ErrInventoryFull) {
		t.Fatalf("unopened capacity used: %v", err)
	}
}
