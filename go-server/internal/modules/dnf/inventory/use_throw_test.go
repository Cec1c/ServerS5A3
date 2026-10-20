package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestThrowUseConsumesSourceUntilEmptyAndRejectsReplayedEmptySlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "6", Slots: map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 111): {ItemID: 6004, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/throw/berry_sky.stk"}},
	}})
	body := make([]byte, 15)
	binary.LittleEndian.PutUint16(body, 111)
	body[2] = listTypeMain
	binary.LittleEndian.PutUint32(body[3:], 0x7428)
	binary.LittleEndian.PutUint32(body[7:], 6004)
	request := alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketUseStackable), Body: body, AccountID: "acc", SelectedCharacterID: 6, Repositories: repos,
		StackableUseProvenanceResolver: stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{
			6004: {ItemID: 6004, PVFPath: "stackable/throw/berry_sky.stk", StackableType: "[throw] 2", Found: true},
		}),
	}
	for remaining := int64(1); remaining >= 0; remaining-- {
		result, err := NewHandler().Handle(ctx, request)
		if err != nil || !result.ResponseAllowed {
			t.Fatalf("throw use failed: %+v %v", result, err)
		}
		loaded := loadTestInventory(t, ctx, repos, "6")
		if loaded.Slots[slotKey(listTypeMain, 111)].Count != remaining {
			t.Fatalf("remaining=%+v want %d", loaded.Slots, remaining)
		}
	}
	result, err := NewHandler().Handle(ctx, request)
	if err != nil || result.ResponseAllowed {
		t.Fatalf("empty-slot replay allowed: %+v %v", result, err)
	}
}
