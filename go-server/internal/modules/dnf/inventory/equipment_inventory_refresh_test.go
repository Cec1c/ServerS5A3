package inventory

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentUnequipRefreshUsesCommittedRedirectedBagSlot(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 10): {ItemID: 900, Count: 1, RawEntry: []byte{0x90}},
		}})
		if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{
			"17": {SlotIndex: 17, ItemID: 700, RawEntry: []byte{0x70}},
		}}); err != nil {
			t.Fatal(err)
		}
		response, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:    uint16(dnfenum.CmdPacketMoveItemspace),
			Body:      currentMoveItemspaceBody(listTypeMain, 10, 0, 1, listTypeEquipment, 17, 0x47E0, 0, -1),
			AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
			EquipmentPlacement:            func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
			EquipmentInventorySlotRefresh: enabled,
		})
		if err != nil || !response.ResponseAllowed {
			t.Fatalf("enabled=%t response=%+v err=%v", enabled, response, err)
		}
		var want []alignedcmd.ItemSlotRefresh
		if enabled {
			want = []alignedcmd.ItemSlotRefresh{{ListType: listTypeMain, SlotIndex: 11}}
		}
		if !reflect.DeepEqual(response.ItemSlotRefreshes, want) {
			t.Fatalf("enabled=%t rows=%v want=%v", enabled, response.ItemSlotRefreshes, want)
		}
		stored := loadTestInventory(t, ctx, repos, "77")
		if stored.Slots[slotKey(listTypeMain, 10)].ItemID != 900 || stored.Slots[slotKey(listTypeMain, 11)].ItemID != 700 {
			t.Fatalf("committed inventory=%v", stored.Slots)
		}
	}
}

func TestEquipmentInventoryRefreshExcludesWornAndNoop(t *testing.T) {
	for _, test := range []struct {
		move dnfequip.MoveResult
		want []alignedcmd.ItemSlotRefresh
	}{
		{dnfequip.MoveResult{Changed: true, SourceListType: 0, SourceSlotIndex: 12, DestinationListType: 3, DestinationSlotIndex: 12}, []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: 12}}},
		{dnfequip.MoveResult{Changed: true, SourceListType: 17, SourceSlotIndex: 1, DestinationListType: 1, DestinationSlotIndex: 5}, []alignedcmd.ItemSlotRefresh{{ListType: 1, SlotIndex: 5}}},
		{dnfequip.MoveResult{Changed: true, SourceListType: 3, SourceSlotIndex: 12, DestinationListType: 3, DestinationSlotIndex: 13}, nil},
		{dnfequip.MoveResult{SourceListType: 0, SourceSlotIndex: 12, DestinationListType: 3, DestinationSlotIndex: 12}, nil},
	} {
		if got := equipmentMoveInventorySlotRefreshes(test.move); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("move=%+v got=%v want=%v", test.move, got, test.want)
		}
	}
}
