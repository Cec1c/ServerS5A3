package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRealPVFPersonalCargoCrystalWithdrawal(t *testing.T) {
	a := real90DropArchive(t)
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pvfItemCatalog: catalog}
	ctx := context.Background()
	for _, id := range []uint32{3033, 3034, 3035, 3036, 3037, 3262} {
		repos := dnfrepomemory.NewMemoryGroup()
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		slot, _ := dnfrepo.AccountWarehouseSlotForItem(int64(id))
		account := dnfrepo.AccountInventoryRecord{AccountID: "cargo", Slots: map[string]dnfrepo.ItemStack{}}
		if _, err := s.mergeCurrentAccountWarehouseStack(&account, definition, slot, 100, nil); err != nil {
			t.Fatal(err)
		}
		stack := account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)]
		stack.Count = 10
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Warehouse: map[string]dnfrepo.ItemStack{"2:4": stack}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.AccountInventory.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 28)
		body[0] = 2
		binary.LittleEndian.PutUint16(body[1:], 4)
		binary.LittleEndian.PutUint32(body[7:], 10)
		binary.LittleEndian.PutUint16(body[12:], 150)
		result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMoveItemspace), Body: body, SelectedCharacterID: 77, AccountID: "cargo", Repositories: repos, InventoryItemPlacement: s.currentInventoryMovePlacement})
		if err != nil || len(result.UpperResponses) != 1 || result.UpperResponses[0].Body[0] != 1 || int16(binary.LittleEndian.Uint16(result.UpperResponses[0].Body[9:])) != slot {
			t.Fatalf("item=%d result=%+v err=%v", id, result, err)
		}
		ch, _, _ := repos.Inventory.Load(ctx, "77")
		got, _, _ := repos.AccountInventory.Load(ctx, "cargo")
		if len(ch.Warehouse) != 0 || len(ch.Slots) != 0 || got.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count != 110 {
			t.Fatalf("item=%d ch=%+v account=%+v", id, ch, got)
		}
	}
}
