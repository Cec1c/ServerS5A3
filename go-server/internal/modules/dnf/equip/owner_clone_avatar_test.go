package equip

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	cloneAvatarTestOrdinary int64 = 101580163
	cloneAvatarTestCloneA   int64 = 42609
	cloneAvatarTestCloneB   int64 = 415500017
	cloneAvatarTestWearSlot int16 = 3
)

// cloneAvatarTestClassifier 是 PVF 分类器的替身，语义与真档一致：
// 普通装扮 = (true,false)、克隆装扮 = (true,true)、其它 = (false,false)。
// 真档判据见 dnfbridge/clone_avatar_test.go 的 real-PVF 用例。
func cloneAvatarTestClassifier(itemID uint32) (bool, bool) {
	switch int64(itemID) {
	case cloneAvatarTestOrdinary:
		return true, false
	case cloneAvatarTestCloneA, cloneAvatarTestCloneB:
		return true, true
	default:
		return false, false
	}
}

func newCloneAvatarTestFixture(t *testing.T) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			inventoryKey(wireListAvatar, 19): {ItemID: cloneAvatarTestCloneA, Count: 0, RawEntry: []byte{0xA1}},
			inventoryKey(wireListAvatar, 20): {ItemID: cloneAvatarTestCloneB, Count: 0, RawEntry: []byte{0xB2}},
		},
	})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			entryKey(cloneAvatarTestWearSlot): {
				SlotIndex: cloneAvatarTestWearSlot,
				ItemID:    cloneAvatarTestOrdinary,
				RawEntry:  []byte{0xC3},
			},
		},
	})
	owner, err := NewOwnerWithPlacementValidator(repos, &recordingPlacementValidator{})
	if err != nil {
		t.Fatalf("NewOwnerWithPlacementValidator error = %v", err)
	}
	return ctx, repos, owner
}

func moveCloneAvatar(t *testing.T, ctx context.Context, owner *Owner, cmd MoveCommand) MoveResult {
	t.Helper()
	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move %+v error = %v", cmd, err)
	}
	return result
}

// TestOwnerMovePersistsInheritsAndClearsCloneAvatarAppearance 钉住装备域的写入契约：
//   - 克隆件顶掉普通装扮 ⇒ 穿戴行记下那件普通装扮的 ItemID；
//   - 克隆件顶掉克隆件 ⇒ 继承后者记下的 id（换第二件克隆件外观不丢）；
//   - 脱下 ⇒ 键从穿戴行和背包行都消失。
//
// 依据：厂商 AvatarDetail.cs:19 + InventoryMoveService.cs:950-1005
// SyncAvatarClearAvatarId；同族参考实现 90原神开源0.1.8
// equip/owner.go:1059-1088 与 owner_clone_avatar_test.go。
func TestOwnerMovePersistsInheritsAndClearsCloneAvatarAppearance(t *testing.T) {
	ctx, repos, owner := newCloneAvatarTestFixture(t)
	owner.SetAvatarClassifier(cloneAvatarTestClassifier)

	equip := func(slot int16) MoveResult {
		t.Helper()
		return moveCloneAvatar(t, ctx, owner, MoveCommand{
			SelectedCharacterID:  77,
			SourceListType:       wireListAvatar,
			SourceSlotIndex:      slot,
			MoveCount:            1,
			DestinationListType:  wireListEquipment,
			DestinationSlotIndex: cloneAvatarTestWearSlot,
		})
	}

	if result := equip(19); result.Mode != "equip_swap" || result.ItemID != cloneAvatarTestCloneA || result.SwappedItemID != cloneAvatarTestOrdinary {
		t.Fatalf("first clone equip result = %+v", result)
	}
	worn := loadTestEquipment(t, ctx, repos, "77").Entries[entryKey(cloneAvatarTestWearSlot)]
	if worn.ItemID != cloneAvatarTestCloneA || worn.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "101580163" {
		t.Fatalf("first clone worn entry = %+v, want appearance key 101580163", worn)
	}
	returned := loadTestInventory(t, ctx, repos, "77").Slots[inventoryKey(wireListAvatar, 19)]
	if returned.ItemID != cloneAvatarTestOrdinary || returned.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "" {
		t.Fatalf("ordinary avatar returned to bag = %+v, want no clone appearance key", returned)
	}

	if result := equip(20); result.Mode != "equip_swap" || result.ItemID != cloneAvatarTestCloneB || result.SwappedItemID != cloneAvatarTestCloneA {
		t.Fatalf("second clone equip result = %+v", result)
	}
	worn = loadTestEquipment(t, ctx, repos, "77").Entries[entryKey(cloneAvatarTestWearSlot)]
	if worn.ItemID != cloneAvatarTestCloneB || worn.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "101580163" {
		t.Fatalf("inherited clone worn entry = %+v, want inherited appearance key 101580163", worn)
	}
	returned = loadTestInventory(t, ctx, repos, "77").Slots[inventoryKey(wireListAvatar, 20)]
	if returned.ItemID != cloneAvatarTestCloneA || returned.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "" {
		t.Fatalf("first clone returned to bag = %+v, want no clone appearance key", returned)
	}

	// 脱下这一格用的是"穿着的那件在**源端装备槽**、目的端是装扮栏背包格"的形状。
	// 2026-09-28 第五轮：原来这里用的是 A 形状（源端装扮栏 21 为空、目的端装备槽指名
	// 那件），它现在由方向判定判 noop —— 客户端在装备端点指名的那件正是那一格穿着的
	// 那件、源端又是空的，那是复述穿戴态，不是脱下请求（见 worn_slot_reassert_gate.go）。
	result := moveCloneAvatar(t, ctx, owner, MoveCommand{
		SelectedCharacterID:      77,
		SourceListType:           wireListEquipment,
		SourceSlotIndex:          cloneAvatarTestWearSlot,
		SourceInstanceValue:      int32(cloneAvatarTestCloneB),
		MoveCount:                1,
		DestinationListType:      wireListAvatar,
		DestinationSlotIndex:     21,
		DestinationInstanceValue: 0,
	})
	if result.Mode != "unequip" {
		t.Fatalf("unequip clone result = %+v, want unequip", result)
	}
	wornEntry, stillWorn := loadTestEquipment(t, ctx, repos, "77").Entries[entryKey(cloneAvatarTestWearSlot)]
	if stillWorn {
		t.Fatalf("clone still worn after unequip = %+v", wornEntry)
	}
	unequipped := loadTestInventory(t, ctx, repos, "77").Slots[inventoryKey(wireListAvatar, 21)]
	if unequipped.ItemID != cloneAvatarTestCloneB || unequipped.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "" {
		t.Fatalf("unequipped clone = %+v, want no clone appearance key", unequipped)
	}
}

// TestOwnerMoveOrdinaryAvatarOverCloneClearsAppearance 钉住"普通装扮顶掉克隆件"
// 这条反向路径：新穿戴行不带键，被顶下来的克隆件回背包也不带键。厂商
// SyncAvatarClearAvatarId 在同一处把 clearAvatarId 归零（InventoryMoveService.cs:967）。
func TestOwnerMoveOrdinaryAvatarOverCloneClearsAppearance(t *testing.T) {
	ctx, repos, owner := newCloneAvatarTestFixture(t)
	owner.SetAvatarClassifier(cloneAvatarTestClassifier)

	// 先让穿戴槽上是"带键的克隆件"，模拟这名玩家此前的穿戴状态。
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			entryKey(cloneAvatarTestWearSlot): {
				SlotIndex: cloneAvatarTestWearSlot,
				ItemID:    cloneAvatarTestCloneA,
				RawEntry:  []byte{0xA1},
				Extra: map[string]string{
					dnfrepo.CloneAvatarAppearanceExtraKey: "101580163",
				},
			},
		},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			inventoryKey(wireListAvatar, 30): {ItemID: cloneAvatarTestOrdinary, Count: 0, RawEntry: []byte{0xC3}},
		},
	})

	result := moveCloneAvatar(t, ctx, owner, MoveCommand{
		SelectedCharacterID:  77,
		SourceListType:       wireListAvatar,
		SourceSlotIndex:      30,
		MoveCount:            1,
		DestinationListType:  wireListEquipment,
		DestinationSlotIndex: cloneAvatarTestWearSlot,
	})
	if result.Mode != "equip_swap" || result.ItemID != cloneAvatarTestOrdinary {
		t.Fatalf("ordinary-over-clone result = %+v", result)
	}
	worn := loadTestEquipment(t, ctx, repos, "77").Entries[entryKey(cloneAvatarTestWearSlot)]
	if worn.ItemID != cloneAvatarTestOrdinary || worn.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "" {
		t.Fatalf("ordinary avatar worn over clone = %+v, want no clone appearance key", worn)
	}
	returned := loadTestInventory(t, ctx, repos, "77").Slots[inventoryKey(wireListAvatar, 30)]
	if returned.ItemID != cloneAvatarTestCloneA || returned.Extra[dnfrepo.CloneAvatarAppearanceExtraKey] != "" {
		t.Fatalf("clone returned to bag = %+v, want no clone appearance key", returned)
	}
}

// TestOwnerMoveWithoutAvatarClassifierWritesNoCloneAppearanceKey 钉住回退档在装备域
// 的语义：dnfbridge 在 DNFBRIDGE_CLONE_AVATAR_APPEARANCE=off 时注入 nil 分类器
// （见 dnfbridge/clone_avatar.go 的 currentCloneAvatarClassifier），此时即使是同样的
// 克隆换装流程，也**不许**出现任何新键 —— 逐字节回到改动前。
func TestOwnerMoveWithoutAvatarClassifierWritesNoCloneAppearanceKey(t *testing.T) {
	ctx, repos, owner := newCloneAvatarTestFixture(t)

	result := moveCloneAvatar(t, ctx, owner, MoveCommand{
		SelectedCharacterID:  77,
		SourceListType:       wireListAvatar,
		SourceSlotIndex:      19,
		MoveCount:            1,
		DestinationListType:  wireListEquipment,
		DestinationSlotIndex: cloneAvatarTestWearSlot,
	})
	if result.Mode != "equip_swap" {
		t.Fatalf("equip_swap result = %+v", result)
	}
	worn := loadTestEquipment(t, ctx, repos, "77").Entries[entryKey(cloneAvatarTestWearSlot)]
	if worn.ItemID != cloneAvatarTestCloneA {
		t.Fatalf("worn entry = %+v, want the clone", worn)
	}
	if _, present := worn.Extra[dnfrepo.CloneAvatarAppearanceExtraKey]; present {
		t.Fatalf("worn entry gained a clone appearance key without a classifier: %+v", worn.Extra)
	}
	returned := loadTestInventory(t, ctx, repos, "77").Slots[inventoryKey(wireListAvatar, 19)]
	if _, present := returned.Extra[dnfrepo.CloneAvatarAppearanceExtraKey]; present {
		t.Fatalf("bag entry gained a clone appearance key without a classifier: %+v", returned.Extra)
	}
}
