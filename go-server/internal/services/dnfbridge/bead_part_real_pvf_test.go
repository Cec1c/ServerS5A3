package dnfbridge

import (
	"context"
	"os"
	"slices"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实运行时 PVF 的部位限制回归（op272）。
//
// PVF 原文证据（/opt/dnf90/data/dnf/Script.pvf 全量取证，1808 个
// `[stackable type] [enchant waste]` 宝珠 / 30 种部位表）：
//   - 宝珠 10008161「黯淡的宠物宝珠 (力量)」→ `[monster card id]` 10006180，卡片
//     `[string data]` = 图标 + `[creature]` ⇒ 只能打宠物部位；
//   - 宝珠 10002536「炎猫妖的尖耳宝珠」→ 卡片部位表 = 图标 + `[shoes]` ⇒ 只能打鞋子；
//   - **没有任何一张卡片同时列出 `[creature]` 与装备部位** ⇒ 同一颗宝珠不可能
//     既打宠物又打装备。
//
// 本用例把真实解析结果直接喂给真实 inventory owner：宠物方向成功并消耗 1，
// 装备方向（武器）明确拒绝且一颗都不消耗。
func TestRealPVFBeadPartRestrictionAcrossPetAndEquipment(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the bead part restriction")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(beadItemID int64, targetItemID int64) (alignedcmd.EnchantBeadResolution, error) {
		return resolveCurrentEnchantBeadMetadata(catalog, catalog.source, beadItemID, targetItemID)
	}

	const petBeadItemID = int64(10008161)
	const petBeadCardID = int64(10006180)
	const shoesBeadItemID = int64(10002536)
	const petItemID = int64(400990168)
	const weaponItemID = int64(35381)
	const mainTargetSlot = int16(30)
	const petTargetSlot = int16(24)
	const characterID = "77"
	const characterNumber = uint16(77)

	// ① 解析器给出的部位证据：宠物宝珠 → [creature]，武器 → [weapon]。
	petResolution, err := resolver(petBeadItemID, petItemID)
	if err != nil {
		t.Fatalf("resolve pet bead on pet: %v", err)
	}
	if petResolution.CardItemID != petBeadCardID || petResolution.TargetKind != "equipment" ||
		petResolution.TargetEquipmentType != "[creature]" ||
		!slices.Contains(petResolution.AllowedEquipmentTypes, "[creature]") {
		t.Fatalf("pet bead resolution = %+v", petResolution)
	}
	weaponResolution, err := resolver(petBeadItemID, weaponItemID)
	if err != nil {
		t.Fatalf("resolve pet bead on weapon: %v", err)
	}
	if weaponResolution.TargetEquipmentType != "[weapon]" ||
		slices.Contains(weaponResolution.AllowedEquipmentTypes, "[weapon]") {
		t.Fatalf("pet bead must not allow the weapon part: %+v", weaponResolution)
	}
	shoesResolution, err := resolver(shoesBeadItemID, petItemID)
	if err != nil {
		t.Fatalf("resolve shoes bead on pet: %v", err)
	}
	if !slices.Contains(shoesResolution.AllowedEquipmentTypes, "[shoes]") ||
		slices.Contains(shoesResolution.AllowedEquipmentTypes, "[creature]") {
		t.Fatalf("shoes bead must not allow the creature part: %+v", shoesResolution)
	}

	// ② 端到端：宠物方向成功且消耗 1。
	t.Run("pet bead on pet succeeds and consumes one", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		saveRealPVFBeadPetFixture(t, ctx, repos, characterID, petBeadItemID, petItemID, petTargetSlot)
		owner, err := dnfinventory.NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.Enchant(ctx, dnfinventory.NewEnchantCommand(alignedcmd.Request{
			AccountID:           "acc",
			SelectedCharacterID: characterNumber,
		}, dnfinventory.EnchantByBeadRequest{
			BeadListType:    dnfrepo.MainInventoryListType,
			BeadSlotIndex:   12,
			TargetListType:  currentPetInventoryListType,
			TargetSlotIndex: petTargetSlot,
		}), resolver)
		if err != nil {
			t.Fatalf("Enchant error = %v", err)
		}
		if !result.Success || !result.Changed || result.CardItemID != petBeadCardID || result.BeadRemainingCount != 1 {
			t.Fatalf("result = %+v", result)
		}
		loaded, found, err := repos.Inventory.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load inventory found=%t err=%v", found, err)
		}
		if got := loaded.Slots["0:12"].Count; got != 1 {
			t.Fatalf("bead count = %d, want 1", got)
		}
		if got := loaded.Slots[currentCeraShopInventorySlotKey(currentPetInventoryListType, petTargetSlot)].Extra["value_a"]; got != "10006180" {
			t.Fatalf("pet target value_a = %q, want 10006180", got)
		}
		petRecord, found, err := repos.Pet.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load pet found=%t err=%v", found, err)
		}
		for _, entry := range petRecord.Entries {
			if entry.ItemID != petItemID {
				continue
			}
			if got := entry.Extra["pet_enchant_card_item_id"]; got != "10006180" {
				t.Fatalf("pet entry enchant = %q, want 10006180", got)
			}
		}
	})

	// ③ 端到端：装备方向（武器）必须明确拒绝，宝珠一颗都不消耗，也不写目标行。
	t.Run("pet bead on a weapon is rejected without consuming", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		saveRealPVFBeadEquipmentFixture(t, ctx, repos, characterID, petBeadItemID, weaponItemID, mainTargetSlot)
		owner, err := dnfinventory.NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.Enchant(ctx, dnfinventory.NewEnchantCommand(alignedcmd.Request{
			AccountID:           "acc",
			SelectedCharacterID: characterNumber,
		}, dnfinventory.EnchantByBeadRequest{
			BeadListType:    dnfrepo.MainInventoryListType,
			BeadSlotIndex:   12,
			TargetListType:  dnfrepo.MainInventoryListType,
			TargetSlotIndex: mainTargetSlot,
		}), resolver)
		if err != nil {
			t.Fatalf("Enchant error = %v", err)
		}
		if result.Success || result.Changed || result.ErrorCode == 0 {
			t.Fatalf("result = %+v, want an explicit rejection", result)
		}
		if result.PartRejection != "bead_part_mismatch" {
			t.Fatalf("part rejection = %q, want bead_part_mismatch", result.PartRejection)
		}
		if result.BeadItemID != petBeadItemID {
			t.Fatalf("rejection must carry bead=%d, got %d", petBeadItemID, result.BeadItemID)
		}
		loaded, found, err := repos.Inventory.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load inventory found=%t err=%v", found, err)
		}
		if got := loaded.Slots["0:12"].Count; got != 2 {
			t.Fatalf("bead count = %d, want 2 (part rejection must not consume)", got)
		}
		target := loaded.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, mainTargetSlot)]
		if got := target.Extra["value_a"]; got != "" {
			t.Fatalf("weapon target written on part rejection: value_a=%q", got)
		}
		if got := target.Extra["enchant_card_id"]; got != "" {
			t.Fatalf("weapon target written on part rejection: enchant_card_id=%q", got)
		}
	})
}

// saveRealPVFBeadPetFixture 造一个最小背包：宝珠 ×2 在 0:12，宠物本体在 list 7 的
// petTargetSlot，并写入同坐标的 PetEntry（与 inventory 包宠物宝珠夹具同形状）。
func saveRealPVFBeadPetFixture(
	t *testing.T,
	ctx context.Context,
	repos dnfrepo.Group,
	characterID string,
	beadItemID int64,
	petItemID int64,
	petTargetSlot int16,
) {
	t.Helper()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots: map[string]dnfrepo.ItemStack{
			"0:12": {
				ItemID: beadItemID,
				Count:  2,
				Extra: map[string]string{
					"item_kind": "stackable",
				},
			},
			currentCeraShopInventorySlotKey(currentPetInventoryListType, petTargetSlot): {
				ItemID:   petItemID,
				Count:    1,
				RawEntry: make([]byte, currentItemListEntryWireSize),
				Extra: map[string]string{
					"creature_serial_or_handle": "37",
					"creature_key":              "37",
					"item_kind":                 "equipment",
					"equipment_type":            "[creature]",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	if err := repos.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: characterID,
		Entries: map[string]dnfrepo.PetEntry{
			"37": {
				PetKey:          "37",
				CreatureKey:     37,
				ItemID:          petItemID,
				SourceListType:  currentPetInventoryListType,
				SourceSlotIndex: petTargetSlot,
				Level:           1,
				Satiety:         100,
				RawEntry:        make([]byte, currentItemListEntryWireSize),
			},
		},
	}); err != nil {
		t.Fatalf("save pet: %v", err)
	}
}

// saveRealPVFBeadEquipmentFixture 造一个最小背包：宝珠 ×2 在 0:12，装备在 0:targetSlot。
func saveRealPVFBeadEquipmentFixture(
	t *testing.T,
	ctx context.Context,
	repos dnfrepo.Group,
	characterID string,
	beadItemID int64,
	targetItemID int64,
	targetSlot int16,
) {
	t.Helper()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots: map[string]dnfrepo.ItemStack{
			"0:12": {
				ItemID: beadItemID,
				Count:  2,
				Extra: map[string]string{
					"item_kind": "stackable",
				},
			},
			currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, targetSlot): {
				ItemID:   targetItemID,
				Count:    1,
				RawEntry: make([]byte, currentItemListEntryWireSize),
				Extra: map[string]string{
					"item_kind": "equipment",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
}
