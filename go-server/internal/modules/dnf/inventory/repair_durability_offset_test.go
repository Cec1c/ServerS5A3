package inventory

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// repairRaw77BagWeapon 造一把 0x77 行形状的背包武器（+0x0A = ext/强化字节、
// +0x0B..0x0C = 耐久字），与线上 101040493 那一行同布局。
func repairRaw77BagWeapon(itemID int64, durability uint16, packedExt byte) []byte {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], 5)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], 999999998)
	raw[0x0A] = packedExt
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

// TestHandlerRepairBagRaw77WeaponWritesTheDurabilityWordTheClientReads 是背包修理的
// 端到端断言：修理 0x77 行背包武器后，客户端真正读的 +0x0B..0x0C 必须变成上限，
// +0x0A（客户端渲染成强化等级）必须一个字节都不变，金币必须被扣。
//
// 旧的写入侧把上限写进 raw[10]（= +0x0A）并把 raw[11]（= +0x0B）清零，等于
// "收钱不修、还把强化等级改掉"。
func TestHandlerRepairBagRaw77WeaponWritesTheDurabilityWordTheClientReads(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	raw := repairRaw77BagWeapon(101040493, 20, 0x0F)
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 5): {
				ItemID:   101040493,
				Count:    1,
				RawEntry: raw,
				Extra: map[string]string{
					"item_kind":      "equipment",
					"durability":     "20",
					"max_durability": "48",
					"pvf_path":       "equipment/character/swordman/weapon/beamsword/101040493.equ",
				},
			},
		},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketRepairEquipment),
		Body: []byte{
			listTypeMain,
			0x05, 0x00,
			0xFF, 0xFF,
			0x00, 0x00, 0x01,
		},
		AccountID:           "acc",
		SelectedCharacterID: 77,
		Repositories:        repos,
		RepairCostResolver:  inventoryRepairTestResolver(48, 4800, 20),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || got.Operation != "repair_equipment" || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	// body 末字节 = quick 修理（现场 repair-all 也是 quick=true）：4800*(20+5)/10 = 12000
	// → 12000*0.08/48*28 = 560，快速修理 ×1.5 = 840 → 5000-840 = 4160 = 0x1040。
	wantBody := []byte{1, 0x40, 0x10, 0x00, 0x00, listTypeMain, 0x05, 0x00, 0x00, 0x00}
	if ack := got.UpperResponses[0]; string(ack.Body) != string(wantBody) {
		t.Fatalf("ack body = % X, want % X", ack.Body, wantBody)
	}

	loaded := loadTestInventory(t, ctx, repos, "77")
	stack := loaded.Slots[slotKey(listTypeMain, 5)]
	if got := stack.RawEntry[0x0A]; got != 0x0F {
		t.Fatalf("raw[0x0A] (强化/ext 字节) = 0x%02X, want 0x0F unchanged", got)
	}
	if got := binary.LittleEndian.Uint16(stack.RawEntry[0x0B:0x0D]); got != 48 {
		t.Fatalf("raw[0x0B:0x0D] durability = %d, want 48", got)
	}
	if got := stack.Extra["durability"]; got != "48" {
		t.Fatalf("extra durability = %q, want 48", got)
	}
	if got := binary.LittleEndian.Uint32(stack.RawEntry[0x0E:0x12]); got != 101040493 {
		t.Fatalf("raw[0x0E:0x12] = %d, want 101040493 untouched", got)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 4160 {
		t.Fatalf("gold = %d, want 4160 (840 quick-repair cost charged)", character.Stats["gold"])
	}
}

// TestHandlerRepairBagRaw77WeaponAliasRowKeepsLegacyOffset 是反向闸门：行长度是 0x77
// 但 itemID 不在 +2（119 字节占位/异形行）时，必须继续按 legacy 偏移 +10 读，
// 不能被新的 0x77 分叉误读成别的东西。
func TestHandlerRepairBagRaw77WeaponAliasRowKeepsLegacyOffset(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	// 全零行（itemID 不在 +2），只有 +0x0B 被 extra 以外的来源写了 20：
	// 旧口径按 legacy 读 raw[10:12] = 0 ⇒ 0 < 48 ⇒ 会修；写入侧必须落在 legacy 偏移。
	raw := make([]byte, 0x77)
	raw[10] = 20
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 6): {
				ItemID:   700,
				Count:    1,
				RawEntry: raw,
				Extra: map[string]string{
					"item_kind":      "equipment",
					"durability":     "20",
					"max_durability": "48",
				},
			},
		},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketRepairEquipment),
		Body: []byte{
			listTypeMain,
			0x06, 0x00,
			0xFF, 0xFF,
			0x00, 0x00, 0x01,
		},
		AccountID:           "acc",
		SelectedCharacterID: 77,
		Repositories:        repos,
		RepairCostResolver:  inventoryRepairTestResolver(48, 4800, 20),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || got.Operation != "repair_equipment" {
		t.Fatalf("result = %+v", got)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	stack := loaded.Slots[slotKey(listTypeMain, 6)]
	if got := binary.LittleEndian.Uint16(stack.RawEntry[10:12]); got != 48 {
		t.Fatalf("legacy-shaped row durability = %d, want 48 written at +10", got)
	}
	if got := binary.LittleEndian.Uint16(stack.RawEntry[0x0B:0x0D]); got != 0 {
		t.Fatalf("legacy-shaped row +0x0B = %d, want 0 untouched", got)
	}
}
