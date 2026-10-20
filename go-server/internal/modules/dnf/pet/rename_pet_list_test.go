package pet

import (
	"bytes"
	"context"
	"testing"

	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 玩家期望（2026-09-24 现场确认）与 A21 参考实现一致：更名卡待在宠物栏消耗品段
// （list 7 的 189..239），改名请求带的就是这一段里的槽位。旧的"固定语义值 7 + 主背包
// 槽位"解释会让宠物栏里的卡永远改不了名——这里钉住新路径，同时保留主背包的历史兼容。
func TestOwnerRenameConsumesCardFromPetConsumableSegment(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seedRenameCreature(t, ctx, repos, "77")
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	// 把卡搬到宠物栏消耗品段（线上角色 3 的 7:190 就是这一格）。
	card := inventory.Slots["0:5"]
	delete(inventory.Slots, "0:5")
	inventory.Slots["7:190"] = card
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	result, err := owner.Rename(ctx, RenameCommand{
		SelectedCharacterID: 77,
		ListType:            listTypePet,
		SlotIndex:           190,
		NameRaw:             []byte("Mina"),
	})
	if err != nil {
		t.Fatalf("Rename from pet consumable segment: %v", err)
	}
	if result.SourceListType != listTypePet || result.SlotIndex != 190 || result.RemainingCount != 1 {
		t.Fatalf("rename result = %+v", result)
	}
	after, _, _ := repos.Inventory.Load(ctx, "77")
	stack, ok := after.Slots["7:190"]
	if !ok || stack.ItemID != currentPetRenameCardItemID || stack.Count != 1 {
		t.Fatalf("pet-list card row after rename = %+v ok=%t", stack, ok)
	}
	record, found, err := repos.Pet.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load pet found=%t err=%v", found, err)
	}
	if entry := record.Entries["37"]; entry.Name != "Mina" || !bytes.Equal(entry.NameRaw, []byte("Mina")) {
		t.Fatalf("renamed entry = %+v", entry)
	}
}
