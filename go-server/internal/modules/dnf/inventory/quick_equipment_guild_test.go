package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestQuickEquipmentGuildWithdrawalLimitAndBagAllocation(t *testing.T) {
	repos, owner, req, rules, now := cargoFixture(t)
	ctx := context.Background()
	_, bag := readCargo(t, repos, req.GuildID)
	bag.Slots["0:9"] = quickEquipmentTestStack(100)
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	rules.Validate = func(r.CharacterRecord, GuildCargoRequest, r.ItemStack, r.ItemStack) error { return nil }
	rules.Placement = func(_ r.CharacterRecord, stack r.ItemStack) (itemplacement.SlotPolicy, error) {
		return quickEquipmentTestPolicy(stack)
	}
	req.Source.Slot, req.SourceItemID, req.Count = 9, 100, 1
	if _, err := owner.TransferGuildCargo(ctx, req, rules, now); err != nil {
		t.Fatal(err)
	}
	g, bag := readCargo(t, repos, req.GuildID)
	bag.Slots["0:3"] = quickEquipmentTestStack(101)
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	beforeCargo, beforeBag := *g.Cargo, r.CloneInventory(bag)
	withdraw := req
	withdraw.Source = req.Destination
	withdraw.Destination = GuildCargoLocation{List: 0, Slot: 4}
	withdraw.DestinationItemID = 0
	if _, err := owner.TransferGuildCargo(ctx, withdraw, rules, now.Add(time.Second)); !errors.Is(err, ErrMoveItemPlacement) {
		t.Fatalf("公会仓库应拒绝第二件：%v", err)
	}
	g, bag = readCargo(t, repos, req.GuildID)
	if !reflect.DeepEqual(beforeCargo, *g.Cargo) || !reflect.DeepEqual(beforeBag.Slots, bag.Slots) {
		t.Fatal("拒绝后公会资产或历史发生变化")
	}
	result, err := owner.WithdrawGuildCargo(ctx, GuildCargoWithdrawalRequest{Owner: req.Owner, GuildID: req.GuildID, CargoSlot: 0, ItemID: 100, Count: 1, DestinationList: 0, Revalidate: req.Revalidate}, rules, now.Add(2*time.Second))
	if err != nil || result.Destination.Slot != 9 {
		t.Fatalf("自动取出应放装备页：%+v %v", result, err)
	}
	_, bag = readCargo(t, repos, req.GuildID)
	if bag.Slots["0:3"].ItemID != 101 || bag.Slots["0:9"].ItemID != 100 {
		t.Fatalf("自动取出改动快捷装备：%+v", bag.Slots)
	}
}
