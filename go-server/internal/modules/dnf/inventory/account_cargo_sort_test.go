package inventory

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAccountCargoSortPreservesInstancesAndOtherContainers(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	accountID := "dnf:sort-account"
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID, Metadata: map[string]string{"account_cargo_level": "8"}}); err != nil {
		t.Fatal(err)
	}
	character := dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: 9, Count: 5}}, Warehouse: map[string]dnfrepo.ItemStack{"12:1": {ItemID: 999, Count: 1}}}
	if err := repos.Inventory.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	rows := map[string]dnfrepo.ItemStack{
		"12:7":  {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment", "unique_id": "abc", "enchant": "3"}},
		"12:4":  {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "equipment", "unique_id": "def", "enchant": "9"}},
		"12:6":  {ItemID: 100, Count: 1, Extra: map[string]string{"item_kind": "equipment", "unique_id": "ghi", "enchant": "7"}},
		"0:354": {ItemID: 3033, Count: 555},
		"12:8":  {ItemID: 700, Count: 1}, // outside the purchased capacity: retain untouched
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: accountID, Slots: rows}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "other", Slots: map[string]dnfrepo.ItemStack{"12:7": {ItemID: 777, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewSortCommand(alignedcmd.Request{AccountID: accountID, SelectedCharacterID: 77}, SortItemRequest{ListType: listTypeAccountCargo, Category: 11})
	result, err := owner.Sort(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.MovedCount != 3 || result.EndSlot != 7 {
		t.Fatalf("result=%+v", result)
	}
	after, _, err := repos.AccountInventory.Load(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dnfrepo.ItemStack{"12:0": rows["12:4"], "12:1": rows["12:6"], "12:2": rows["12:7"], "0:354": rows["0:354"], "12:8": rows["12:8"]}
	if !reflect.DeepEqual(after.Slots, want) {
		t.Fatalf("instances or non-cargo rows changed: %+v", after.Slots)
	}
	charAfter, _, _ := repos.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(charAfter, character) {
		t.Fatal("per-character inventory was modified")
	}
	other, _, _ := repos.AccountInventory.Load(ctx, "other")
	if other.Slots["12:7"].ItemID != 777 {
		t.Fatal("other account modified")
	}
	again, err := owner.Sort(ctx, cmd)
	if err != nil || again.Changed {
		t.Fatalf("repeated sort: %+v %v", again, err)
	}
}

func TestAccountCargoSortHandlerAcknowledgesBeforeDedicatedAccountRefresh(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	for _, err := range []error{
		repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}}),
		repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77"}),
		repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"12:7": {ItemID: 100, Count: 2}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketSortItem), Body: []byte{6, 0, 0, 0, 16, 12, 24, 11, 32, 0}, AccountID: "a", SelectedCharacterID: 77, Repositories: repos})
	if err != nil || !result.Handled || !result.ResponseAllowed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.UpperResponses) != 1 || result.UpperResponses[0].MsgID != 20 || !reflect.DeepEqual(result.UpperResponses[0].Body, buildSortItemAck(12)) {
		t.Fatalf("ack=%+v", result.UpperResponses)
	}
	if !reflect.DeepEqual(result.PostActions, []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedAccountCargo}) {
		t.Fatalf("refresh=%+v", result.PostActions)
	}
}
