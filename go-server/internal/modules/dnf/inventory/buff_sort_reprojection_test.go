package inventory

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMainBagSortReprojectsExistingBuffAfterReplacingList(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "account77",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]repo.ItemStack{
			"0:20": {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
			"0:21": {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, repos, "77", "account77", buffswap.Command{
		Skill: 38,
		Slot:  12,
		List:  listTypeMain,
		Index: 20,
	}); err != nil {
		t.Fatal(err)
	}
	persistedBefore, _, err := repos.Settings.Load(ctx, buffswap.Scope("77"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := buffswap.LoadResolvedOwned(ctx, repos, "77", "account77")
	if err != nil {
		t.Fatal(err)
	}
	instance := before.State.Slots[12].Instance
	if instance == "" || before.State.Slots[12].Index != 20 {
		t.Fatalf("fixture registration=%+v", before.State)
	}

	sortRequest := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSortItem),
		Body:                []byte{listTypeMain, 1},
		AccountID:           "account77",
		SelectedCharacterID: 77,
		Repositories:        repos,
	}
	first, err := NewHandler().Handle(ctx, sortRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Handled || !first.ResponseAllowed || !first.RefreshSelectedBuffRegistration {
		t.Fatalf("changed main-bag sort did not request Buff reprojection: %+v", first)
	}
	if len(first.UpperResponses) != 2 || first.UpperResponses[0].MsgID != uint16(dnfenum.CmdPacketSortItem) ||
		first.UpperResponses[1].MsgID != msgItemListRefresh || first.UpperResponses[1].Classification != 0 {
		t.Fatalf("changed sort responses=%+v", first.UpperResponses)
	}

	inv, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if inv.Slots["0:9"].ItemID != 100 || inv.Slots["0:10"].ItemID != 200 ||
		inv.Slots["0:10"].Extra["native_buff_instance"] != instance {
		t.Fatalf("real main-bag reorder did not preserve registered instance: %+v", inv.Slots)
	}
	after, err := buffswap.LoadResolvedOwned(ctx, repos, "77", "account77")
	if err != nil {
		t.Fatal(err)
	}
	binding := after.State.Slots[12]
	if after.State.Skill != 38 || binding.Instance != instance || binding.List != listTypeMain || binding.Index != 10 {
		t.Fatalf("resolved registration after sort=%+v", after.State)
	}
	persistedAfter, _, err := repos.Settings.Load(ctx, buffswap.Scope("77"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedBefore, persistedAfter) {
		t.Fatal("sort created or rewrote the Buff registration")
	}

	second, err := NewHandler().Handle(ctx, sortRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Handled || !second.ResponseAllowed || !second.RefreshSelectedBuffRegistration {
		t.Fatalf("unchanged repeated sort still replaces native bag objects: %+v", second)
	}
	repeated, err := buffswap.LoadResolvedOwned(ctx, repos, "77", "account77")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.State.Skill != 38 || repeated.State.Slots[12] != binding {
		t.Fatalf("repeated sort changed registration: before=%+v after=%+v", binding, repeated.State.Slots[12])
	}
}
