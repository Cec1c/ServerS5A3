package equip

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 回归钉：GM 面板邮件领取落地的装备行没有 0x77 raw 行，op19 穿上时必须靠
// "耐久 + 品质种子 + item_kind/pvf_path" 重建 raw 行。缺了 quality_seed，
// 重建失败 → 事务内 ErrMoveRawEntryMissing → 服务端静默不回包，客户端本地
// 乐观穿上、重登又掉下来（操作者看到的"武器自动脱下"）。
const (
	mailClaimedEquipmentExtraID int64  = 101000069
	mailClaimedEquipmentSeed    uint32 = 123456789
	mailClaimedEquipmentDura    uint16 = 57
	mailClaimedEquipWeaponSlot  int16  = 12
	mailClaimedSourceInstance   int32  = 0x05F8A08C
)

func mailClaimedEquipmentExtra(withQualitySeed bool) map[string]string {
	extra := map[string]string{
		// 邮件领取路径写入的证据：身份 + 耐久（dnfbridge 的
		// mailboxItemResolutionFromPVF / mailbox_durability_projection.go）。
		"item_kind":      "equipment",
		"pvf_path":       "equipment/weapon/101000069.equ",
		"equipment_type": "[weapon]",
		"durability":     "57",
		"max_durability": "57",
	}
	if withQualitySeed {
		extra["quality_seed"] = "123456789"
	}
	return extra
}

func TestMailClaimedEquipmentMoveRebuildsRawFromDurabilityAndQualitySeed(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9": {
				ItemID: mailClaimedEquipmentExtraID,
				Count:  1,
				Extra:  mailClaimedEquipmentExtra(true),
			},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      77,
		SourceListType:           wireListMain,
		SourceSlotIndex:          9,
		SourceInstanceValue:      mailClaimedSourceInstance,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     mailClaimedEquipWeaponSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v, want a committed equip from the claimed durability/identity evidence", err)
	}
	if !result.Changed || result.Mode != "equip" || result.ItemID != mailClaimedEquipmentExtraID {
		t.Fatalf("result = %+v", result)
	}
	entry, found := loadTestEquipment(t, ctx, repos, "77").Entries["12"]
	if !found {
		t.Fatalf("worn weapon slot 12 missing: %+v", loadTestEquipment(t, ctx, repos, "77").Entries)
	}
	if len(entry.RawEntry) != currentEquipmentRawEntryWireSize {
		t.Fatalf("worn raw entry size = %d, want 0x%X", len(entry.RawEntry), currentEquipmentRawEntryWireSize)
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0:2]); got != uint16(mailClaimedEquipWeaponSlot) {
		t.Fatalf("raw slot = %d, want %d", got, mailClaimedEquipWeaponSlot)
	}
	if got := binary.LittleEndian.Uint32(entry.RawEntry[2:6]); got != uint32(mailClaimedEquipmentExtraID) {
		t.Fatalf("raw item = %d", got)
	}
	if got := binary.LittleEndian.Uint32(entry.RawEntry[6:10]); got != mailClaimedEquipmentSeed {
		t.Fatalf("raw quality seed = %d, want %d (the claimed instance identity)", got, mailClaimedEquipmentSeed)
	}
	if got := binary.LittleEndian.Uint16(entry.RawEntry[0x0B:0x0D]); got != mailClaimedEquipmentDura {
		t.Fatalf("raw durability = %d, want %d", got, mailClaimedEquipmentDura)
	}
	if got := binary.LittleEndian.Uint32(entry.RawEntry[0x0E:0x12]); got != uint32(mailClaimedSourceInstance) {
		t.Fatalf("raw source identity = %#x, want the op19 source identity %#x", got, uint32(mailClaimedSourceInstance))
	}
	// 装备槽必须真的落库，背包行必须消失（否则就是"本地穿上、重登掉下来"）。
	loaded := loadTestInventory(t, ctx, repos, "77")
	if _, ok := loaded.Slots["0:9"]; ok {
		t.Fatalf("claimed stack is still in the bag after the equip committed: %+v", loaded.Slots)
	}
}

// 同一行缺 quality_seed 时必须**继续失败关闭**：修复是"把证据补齐"，不是放宽校验。
// 这条同时是"GM 发的装备穿上被静默拒绝"的根因钉子。
func TestMailClaimedEquipmentMoveStillFailsWithoutQualitySeed(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:9": {
				ItemID: mailClaimedEquipmentExtraID,
				Count:  1,
				Extra:  mailClaimedEquipmentExtra(false),
			},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: map[string]dnfrepo.EquipmentEntry{}})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	_, err = owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      77,
		SourceListType:           wireListMain,
		SourceSlotIndex:          9,
		SourceInstanceValue:      mailClaimedSourceInstance,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     mailClaimedEquipWeaponSlot,
		DestinationInstanceValue: 0,
	})
	if !errors.Is(err, ErrMoveRawEntryMissing) {
		t.Fatalf("Move error = %v, want %v (the gate must not be relaxed)", err, ErrMoveRawEntryMissing)
	}
	if entries := loadTestEquipment(t, ctx, repos, "77").Entries; len(entries) != 0 {
		t.Fatalf("rejected move still wrote worn entries: %+v", entries)
	}
	if _, ok := loadTestInventory(t, ctx, repos, "77").Slots["0:9"]; !ok {
		t.Fatal("rejected move removed the source stack")
	}
}
