package dnfbridge

import (
	"context"
	"fmt"
	"strings"

	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentActorMode0AppearanceSlotCount = 14
	currentActorMode0AppearanceRowBytes  = 19
	currentActorMode0AppearanceEmptyItem = ^uint32(0)
	currentActorMode0WeaponSlot          = 12
	currentActorMode0ReinforceMax        = 0x7f
)

// buildCurrentActorMode0AppearanceSnapshot loads the authoritative worn-item
// record and returns the complete current-EXE base-slot-record snapshot. A
// found record always produces all slots 0..13, including explicit empty rows
// that clear stale client appearance state after an item is removed.
func buildCurrentActorMode0AppearanceSnapshot(
	ctx context.Context,
	repo dnfrepo.EquipmentRepository,
	characterID string,
) ([]byte, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if repo == nil || characterID == "" {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	record, found, err := repo.Load(ctx, characterID)
	if err != nil || !found {
		return nil, found, err
	}
	body, err := buildCurrentActorMode0AppearanceSnapshotFromEquipment(record)
	if err != nil {
		return nil, true, err
	}
	return body, true, nil
}

func buildCurrentActorMode0AppearanceSnapshotFromEquipment(record dnfrepo.EquipmentRecord) ([]byte, error) {
	rows, err := buildCurrentActorMode0AppearanceSummaryFromEquipment(record)
	if err != nil {
		return nil, err
	}
	var writer packetWriter
	writeCurrentSceneObjectEquipSummary(&writer, rows)
	return writer.bytes(), nil
}

// buildCurrentActorMode0AppearanceSummaryFromEquipment returns the complete
// current-EXE base appearance table. All fourteen rows are present so an empty
// slot explicitly removes stale client state left by an earlier mode0 update.
func buildCurrentActorMode0AppearanceSummaryFromEquipment(record dnfrepo.EquipmentRecord) ([]dnfrepo.CharacterRosterEquipSummary, error) {
	itemIDs := [currentActorMode0AppearanceSlotCount]uint32{}
	packedFlags := [currentActorMode0AppearanceSlotCount]byte{}
	// linkItemIDs 是这一行 19 字节外观记录的 LinkItemId（`writeCurrentSceneObjectEquipSummary`
	// 的 row.OptionalIDOrExpire，偏移 +10）。克隆装扮在这里写"真正拥有的那件（=克隆件自己）"，
	// 与 itemIDs（被克隆件）成对；非克隆行恒 0。取值点与名册载体共用，见
	// party_peer_appearance.go 的文件头（队友时装丢失根因）与 clone_avatar.go:132-166。
	linkItemIDs := [currentActorMode0AppearanceSlotCount]uint32{}
	occupied := [currentActorMode0AppearanceSlotCount]bool{}
	for slot := range itemIDs {
		itemIDs[slot] = currentActorMode0AppearanceEmptyItem
	}

	extraRows := make([]dnfrepo.CharacterRosterEquipSummary, 0, 1)
	for _, equipped := range record.Entries {
		if equipped.ItemID <= 0 {
			continue
		}
		appearanceSlot, ok := currentActorMode0AppearanceSlot(equipped)

		if !ok {
			continue
		}
		if appearanceSlot >= currentActorMode0AppearanceSlotCount {
			if appearanceSlot == uint64(dnfguardianshield.ActorSlot) && dnfguardianshield.IsMainProjectionEntry(equipped) {
				extraRows = append(extraRows, dnfrepo.CharacterRosterEquipSummary{Slot: int64(appearanceSlot), ItemIDOrIcon: int64(currentActorAppearanceItemID(equipped))})
			}
			continue
		}
		if equipped.ItemID > int64(^uint32(0)) {
			return nil, fmt.Errorf("current actor mode0 appearance item id %d exceeds uint32", equipped.ItemID)
		}
		slot := int(appearanceSlot)
		if occupied[slot] {
			return nil, fmt.Errorf("current actor mode0 appearance slot %d has duplicate equipment", slot)
		}
		occupied[slot] = true
		itemIDs[slot] = currentActorAppearanceItemID(equipped)
		linkItemIDs[slot] = currentSceneObjectAppearanceRowLinkItemID(equipped)
		if slot == currentActorMode0WeaponSlot {
			packedFlags[slot] = currentActorMode0WeaponPackedFlags(equipped)
		}
	}

	rows := make([]dnfrepo.CharacterRosterEquipSummary, currentActorMode0AppearanceSlotCount)
	for slot, itemID := range itemIDs {
		rows[slot] = dnfrepo.CharacterRosterEquipSummary{
			Slot:               int64(slot),
			ItemIDOrIcon:       int64(itemID),
			PackedFlags:        int64(packedFlags[slot]),
			OptionalIDOrExpire: int64(linkItemIDs[slot]),
		}
	}
	return append(rows, extraRows...), nil
}

// currentActorAppearanceItemID 是 mode0 外观表与名册摘要共用的"这一格该显示成
// 哪件道具"取值点。
//
// 一件「稀有克隆装扮」穿在身上时，穿戴行的 ItemID 是克隆件本身，而它的 PVF 模板
// 带 `[item category] clear avatar`（透明件）——直接把穿戴行 ItemID 发下去，客户端
// 就什么都不画。厂商在投影层做替换：`Noti2InventoryProjectionBuilder.cs:250-257`
// `ResolveAppearanceItemId` 返回 `AvatarDetail.ClearAvatarId`，同族参考实现
// 90原神开源0.1.8 `dnfbridge/current_actor_mode0_appearance.go:101-106` 用同一个键。
//
// 键缺失、或 DNFBRIDGE_CLONE_AVATAR_APPEARANCE 取回退值时返回穿戴行自己的 ItemID，
// 与改动前逐字节一致。
func currentActorAppearanceItemID(equipped dnfrepo.EquipmentEntry) uint32 {
	if linked := cloneAvatarAppearanceLinkedItemID(equipped); linked != 0 {
		return linked
	}
	return sceneInventoryUint32FromInt64(equipped.ItemID)
}

func currentActorMode0WeaponPackedFlags(equipped dnfrepo.EquipmentEntry) byte {
	reinforce := currentItemListEquipmentExtData(equipped)
	if reinforce > currentActorMode0ReinforceMax {
		reinforce = currentActorMode0ReinforceMax
	}
	// sub_20026C0 stores packed_byte>>1 as the per-slot value and bit 0 as
	// a separate flag. Reinforcement occupies the value portion; the unrelated
	// flag remains clear for the authoritative worn-weapon projection.
	return reinforce << 1
}

func currentActorMode0AppearanceSlot(equipped dnfrepo.EquipmentEntry) (uint64, bool) {
	if slot, ok := sceneInventoryExtraUint(
		equipped.Extra,
		"current_exe_equipment_type",
		"current_exe_appearance_slot",
		"appearance_slot",
	); ok {
		return slot, true
	}

	// The same-EXE runtime wire uses direct appearance slots 0..13. Legacy Go
	// PVF creation records instead store worn slots, so only those records use
	// the existing worn-slot conversion (11->12, 13..22->14..23, 23->25).
	if !isPVFInitialEquipment(equipped) && equipped.SlotIndex >= 0 && equipped.SlotIndex < currentActorMode0AppearanceSlotCount {
		return uint64(equipped.SlotIndex), true
	}
	return currentEXEActorEquipmentSlot(equipped)
}
