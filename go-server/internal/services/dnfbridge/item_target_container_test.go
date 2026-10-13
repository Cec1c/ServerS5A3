package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 「道具发放落到哪个容器 / 哪一段」的唯一判定回归。
//
// 现场两条报障都在这一类：
//  1. 商城新买的宠物更名卡（item 25，`[stackable type] [creature]`）落在主背包消耗品页
//     （65..120），而邮件领取的同名卡在宠物消耗品段（list 7 189..238）；
//  2. 材料类道具入包时进了非材料段（`[material instant item]` 只因为家族判定写死了带右括号的
//     `[material]` 而掉进默认的消耗品页；教程奖励更是整段硬写 65..120）。
//
// 真实家族与 ID 都来自运行时 Script.pvf 全量取证（见 item_target_container.go 注释）。
func TestCurrentPVFItemTargetContainerFollowsRuntimePVFFamily(t *testing.T) {
	tests := []struct {
		name           string
		definition     dungeonDropItemDefinition
		wantList       byte
		wantStart      int16
		wantEnd        int16
		wantPetConsum  bool
		wantPetBody    bool
		wantPetArtfact bool
		wantAvatar     bool
	}{
		{
			name:          "宠物饲料 [feed]（item 24）→ list 7 189..238",
			definition:    dungeonDropItemDefinition{ItemID: 24, Kind: dungeonDropItemStackable, StackableType: "`[feed]`", PVFPath: "stackable/cash/creature/creature_food.stk"},
			wantList:      currentPetInventoryListType,
			wantStart:     currentPVFPetConsumableSlotStart,
			wantEnd:       currentPVFPetConsumableSlotEnd,
			wantPetConsum: true,
		},
		{
			name:          "宠物更名卡 [creature]（item 25）→ list 7 189..238",
			definition:    dungeonDropItemDefinition{ItemID: 25, Kind: dungeonDropItemStackable, StackableType: "[creature]", PVFPath: "stackable/cash/creature/rename_card.stk"},
			wantList:      currentPetInventoryListType,
			wantStart:     currentPVFPetConsumableSlotStart,
			wantEnd:       currentPVFPetConsumableSlotEnd,
			wantPetConsum: true,
		},
		{
			name:       "材料 [material]（item 3277）→ 主背包 121..176",
			definition: dungeonDropItemDefinition{ItemID: 3277, Kind: dungeonDropItemStackable, StackableType: "[material]", PVFPath: "stackable/material/material_3277.stk"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainMaterialStart,
			wantEnd:    currentPVFMainMaterialEnd,
		},
		{
			name:       "材料 [material instant item]（item 6515 fp/fpcubepiece_clear.stk）→ 主背包 121..176",
			definition: dungeonDropItemDefinition{ItemID: 6515, Kind: dungeonDropItemStackable, StackableType: "[material instant item]", PVFPath: "stackable/fp/fpcubepiece_clear.stk"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainMaterialStart,
			wantEnd:    currentPVFMainMaterialEnd,
		},
		{
			name:       "副职业材料 [material expert job] → 主背包 233..288",
			definition: dungeonDropItemDefinition{ItemID: 10091936, Kind: dungeonDropItemStackable, StackableType: "[material expert job]"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainExpertJobStart,
			wantEnd:    currentPVFMainExpertJobEnd,
		},
		{
			name:       "任务品 [quest] → 主背包 177..232",
			definition: dungeonDropItemDefinition{ItemID: 10403, Kind: dungeonDropItemStackable, StackableType: "[quest]"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainQuestStart,
			wantEnd:    currentPVFMainQuestEnd,
		},
		{
			name:       "徽章 [avatar emblem] → 主背包 289..344",
			definition: dungeonDropItemDefinition{ItemID: 10303480, Kind: dungeonDropItemStackable, StackableType: "[avatar emblem]"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainEmblemStart,
			wantEnd:    currentPVFMainEmblemEnd,
		},
		{
			name:       "普通消耗品 [waste] → 主背包 65..120",
			definition: dungeonDropItemDefinition{ItemID: 42, Kind: dungeonDropItemStackable, StackableType: "[waste]"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainConsumableStart,
			wantEnd:    currentPVFMainConsumableEnd,
		},
		{
			name:        "宠物本体（[creature] 装备）→ list 7 0..139",
			definition:  dungeonDropItemDefinition{ItemID: 400990168, Kind: dungeonDropItemEquipment, EquipmentType: "[creature]"},
			wantList:    currentPetInventoryListType,
			wantStart:   currentPVFPetBodySlotStart,
			wantEnd:     currentPVFPetBodySlotEnd,
			wantPetBody: true,
		},
		{
			name:           "宠物装备 artifact red → list 7 140..188",
			definition:     dungeonDropItemDefinition{ItemID: 63521, Kind: dungeonDropItemEquipment, EquipmentType: "[artifact red]"},
			wantList:       currentPetInventoryListType,
			wantStart:      currentPVFPetArtifactSlotStart,
			wantEnd:        currentPVFPetArtifactSlotEnd,
			wantPetArtfact: true,
		},
		{
			name:       "时装 [coat avatar] → 装扮栏 list 1",
			definition: dungeonDropItemDefinition{ItemID: 40220, Kind: dungeonDropItemEquipment, EquipmentType: "[coat avatar]"},
			wantList:   1,
			wantStart:  currentPVFAvatarSlotStart,
			wantEnd:    currentPVFAvatarSlotEnd,
			wantAvatar: true,
		},
		{
			name:       "普通装备 → 主背包 9..64（装备段）",
			definition: dungeonDropItemDefinition{ItemID: 35381, Kind: dungeonDropItemEquipment, EquipmentType: "[weapon]"},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainEquipmentSlotStart,
			wantEnd:    currentPVFMainEquipmentSlotEnd,
		},
		{
			name:       "未知 kind → 消耗品页兜底",
			definition: dungeonDropItemDefinition{ItemID: 1},
			wantList:   dnfrepo.MainInventoryListType,
			wantStart:  currentPVFMainConsumableStart,
			wantEnd:    currentPVFMainConsumableEnd,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			container := currentPVFItemTargetContainer(test.definition)
			if container.ListType != test.wantList || container.SlotStart != test.wantStart || container.SlotEnd != test.wantEnd {
				t.Fatalf("container = %+v, want list=%d slots=%d..%d", container, test.wantList, test.wantStart, test.wantEnd)
			}
			if container.PetConsumable != test.wantPetConsum || container.PetBody != test.wantPetBody ||
				container.PetArtifact != test.wantPetArtfact || container.Avatar != test.wantAvatar {
				t.Fatalf("container flags = %+v", container)
			}
			// 材料/消耗品永远不得落进装备段（现场报障"材料会进入装备"）。
			if test.definition.Kind == dungeonDropItemStackable &&
				container.ListType == dnfrepo.MainInventoryListType &&
				container.SlotStart >= currentPVFMainEquipmentSlotStart && container.SlotEnd <= currentPVFMainEquipmentSlotEnd {
				t.Fatalf("stackable family %q landed in the equipment segment: %+v", test.definition.StackableType, container)
			}
		})
	}
}

// 宠物更名卡（item 25）必须经**每一条**发放路径都进宠物消耗品段（list 7 189..238）。
func TestPetRenameCardGrantPathsTargetPetConsumableSegment(t *testing.T) {
	renameCard := dungeonDropItemDefinition{
		ItemID:        25,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/creature/rename_card.stk",
		StackableType: "[creature]",
	}

	t.Run("家族判据本身认 [creature]", func(t *testing.T) {
		if !isCurrentCeraShopPetConsumable(renameCard) {
			t.Fatal("rename card must be a pet consumable")
		}
		if !currentPVFItemTargetContainer(renameCard).PetConsumable {
			t.Fatal("rename card container must be the pet consumable segment")
		}
	})

	t.Run("点券商城/礼包/booster 落库助手：list 7 189", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{CharacterID: "19"}
		slots, err := grantCurrentCeraShopPetConsumable(&inventory, renameCard, 2)
		if err != nil {
			t.Fatalf("grantCurrentCeraShopPetConsumable error = %v", err)
		}
		if !reflect.DeepEqual(slots, []int16{currentPVFPetConsumableSlotStart}) {
			t.Fatalf("slots = %v, want [189]", slots)
		}
		stack, found := inventory.Slots[currentCeraShopInventorySlotKey(currentPetInventoryListType, currentPVFPetConsumableSlotStart)]
		if !found || stack.ItemID != 25 || stack.Count != 2 {
			t.Fatalf("pet consumable row = %+v found=%t", stack, found)
		}
		if _, leaked := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, currentPVFPetConsumableSlotStart)]; leaked {
			t.Fatal("rename card leaked into main list at the pet-consumable slot index")
		}
	})

	t.Run("booster 礼盒：placement = 宠物消耗品", func(t *testing.T) {
		placement, err := currentBoosterPlacement(renameCard)
		if err != nil {
			t.Fatalf("currentBoosterPlacement error = %v", err)
		}
		if placement != currentBoosterRewardPetConsumable {
			t.Fatalf("placement = %v, want pet consumable", placement)
		}
	})

	t.Run("邮件领取：resolution 指向 list 7 189..238", func(t *testing.T) {
		resolution, err := mailboxItemResolutionFromPVF(renameCard, false)
		if err != nil {
			t.Fatalf("mailboxItemResolutionFromPVF error = %v", err)
		}
		if resolution.TargetListType != currentPetInventoryListType ||
			resolution.SlotStart != currentPVFPetConsumableSlotStart ||
			resolution.SlotEnd != currentPVFPetConsumableSlotEnd {
			t.Fatalf("mail resolution = %+v", resolution)
		}
	})

	t.Run("魔盒/随机奖励：奖励投影指向 list 7 189..238", func(t *testing.T) {
		catalog, source := newItemContainerTestCatalog(t)
		item, err := resolveCurrentMagicBoxRewardItem(catalog, source, 25)
		if err != nil {
			t.Fatalf("resolveCurrentMagicBoxRewardItem error = %v", err)
		}
		if item.TargetListType != currentPetInventoryListType ||
			item.SlotStart != currentPVFPetConsumableSlotStart ||
			item.SlotEnd != currentPVFPetConsumableSlotEnd {
			t.Fatalf("magic box rename card = %+v", item)
		}
		resolution, err := resolveCurrentRandomRewardItem(catalog, source, 4242)
		if err != nil {
			t.Fatalf("resolveCurrentRandomRewardItem error = %v", err)
		}
		var granted *alignedcmd.MagicBoxRewardItem
		for index := range resolution.Outcomes {
			if resolution.Outcomes[index].Reward.ItemID == 25 {
				granted = &resolution.Outcomes[index].Reward
			}
		}
		if granted == nil {
			t.Fatalf("random reward outcomes = %+v, want item 25", resolution.Outcomes)
		}
		if granted.TargetListType != currentPetInventoryListType ||
			granted.SlotStart != currentPVFPetConsumableSlotStart || granted.SlotEnd != currentPVFPetConsumableSlotEnd {
			t.Fatalf("random reward rename card = %+v", *granted)
		}
	})
}

// 材料类道具入包必须进材料段（121..176），不得进装备段（9..64）也不得进消耗品页（65..120）。
func TestMaterialGrantPathsTargetMaterialSegment(t *testing.T) {
	material := dungeonDropItemDefinition{
		ItemID:        6515,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/fp/fpcubepiece_clear.stk",
		StackableType: "[material instant item]",
		StackLimit:    1000,
		SlotStart:     currentPVFMainMaterialStart,
		SlotEnd:       currentPVFMainMaterialEnd,
	}
	plainMaterial := dungeonDropItemDefinition{
		ItemID:        3277,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/material/material_3277.stk",
		StackableType: "[material]",
		StackLimit:    999,
		SlotStart:     currentPVFMainMaterialStart,
		SlotEnd:       currentPVFMainMaterialEnd,
	}

	t.Run("分段函数把材料家族都映射到 121..176", func(t *testing.T) {
		for _, stackableType := range []string{"[material]", "[material instant item]"} {
			start, end := dungeonDropStackableSlotRange(stackableType)
			if start != currentPVFMainMaterialStart || end != currentPVFMainMaterialEnd {
				t.Fatalf("family %q slots = %d..%d, want %d..%d", stackableType, start, end,
					currentPVFMainMaterialStart, currentPVFMainMaterialEnd)
			}
		}
	})

	t.Run("发放落库：快捷格空着时材料仍进入材料页", func(t *testing.T) {
		for _, definition := range []dungeonDropItemDefinition{material, plainMaterial} {
			inventory := dnfrepo.InventoryRecord{CharacterID: "19"}
			slots, err := grantCurrentCeraShopProduct(&inventory, definition, 3)
			if err != nil {
				t.Fatalf("item=%d grantCurrentCeraShopProduct error = %v", definition.ItemID, err)
			}
			if len(slots) != 1 {
				t.Fatalf("item=%d slots = %v, want one granted row", definition.ItemID, slots)
			}
			slot := int16(slots[0])
			if slot != currentPVFMainMaterialStart {
				t.Fatalf("item=%d landed at slot %d, want material slot 121", definition.ItemID, slot)
			}
			stack, found := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]
			if !found || stack.ItemID != int64(definition.ItemID) || stack.Count != 3 {
				t.Fatalf("item=%d granted row = %+v found=%t", definition.ItemID, stack, found)
			}
		}
	})

	t.Run("快捷格占满后材料落进材料段 121..176", func(t *testing.T) {
		for _, definition := range []dungeonDropItemDefinition{material, plainMaterial} {
			inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}
			// 快捷格 3..8 先占满（不属于材料家族，避免影响身份合并）。
			for slot := int16(3); slot <= 8; slot++ {
				inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] =
					dnfrepo.ItemStack{ItemID: 42, Count: 1}
			}
			slots, err := grantCurrentCeraShopProduct(&inventory, definition, 3)
			if err != nil {
				t.Fatalf("item=%d grantCurrentCeraShopProduct error = %v", definition.ItemID, err)
			}
			if len(slots) != 1 {
				t.Fatalf("item=%d slots = %v, want one material row", definition.ItemID, slots)
			}
			slot := int16(slots[0])
			if slot < currentPVFMainMaterialStart || slot > currentPVFMainMaterialEnd {
				t.Fatalf("item=%d landed at slot %d, want the material segment %d..%d", definition.ItemID, slot,
					currentPVFMainMaterialStart, currentPVFMainMaterialEnd)
			}
			stack, found := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]
			if !found || stack.ItemID != int64(definition.ItemID) || stack.Count != 3 {
				t.Fatalf("item=%d material row = %+v found=%t", definition.ItemID, stack, found)
			}
		}
	})
}

// 端到端：**点券商城购买**宠物更名卡（item 25，`[creature]`）必须落进 list 7 的
// 宠物消耗品段（现场报障：商城新买的更名卡掉进了主背包消耗品/材料栏，而邮件领的在同一段）。
func TestCurrentCeraShopPurchaseRoutesPetRenameCardToPetConsumables(t *testing.T) {
	catalog := newCeraShopRenameCardTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	seedCeraShopAccountCera(t, ctx, repositories, "account-rename-card", 100)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "24",
		AccountID:   "account-rename-card",
		Stats:       map[string]int64{"cera": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "24",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountID: "account-rename-card"},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	request, err := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(100059))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.commitCurrentCeraShopPurchase(ctx, &gameSession{selectedCharacterID: 24}, catalog, request)
	if err != nil {
		t.Fatalf("commit pet rename card purchase: %v", err)
	}
	if !result.PetInventoryChanged {
		t.Fatalf("rename card purchase did not request a pet-container refresh: %+v", result)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "24")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	card, found := inventory.Slots[currentCeraShopInventorySlotKey(currentPetInventoryListType, currentPVFPetConsumableSlotStart)]
	if !found || card.ItemID != 25 || card.Count != 1 || card.Extra["stackable_type"] != "[creature]" {
		t.Fatalf("pet rename card row = %+v found=%t (inventory=%+v)", card, found, inventory.Slots)
	}
	if _, leaked := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, currentPVFMainConsumableStart)]; leaked {
		t.Fatalf("rename card leaked into the main consumable page: %+v", inventory.Slots)
	}
	if len(inventory.Slots) != 1 {
		t.Fatalf("unexpected extra rows after the rename card purchase: %+v", inventory.Slots)
	}
}

// newCeraShopRenameCardTestCatalog 造一个最小点券商城目录：commodity 100059 → item 25
// （`cash/creature/rename_card.stk`，家族 `[creature]`，无 [stack limit]，与运行时 PVF 同形状）。
func newCeraShopRenameCardTestCatalog(t *testing.T) *pvfCeraShopCatalog {
	t.Helper()
	source := bridgePVFSource{
		"etc/cerashop.etc": "[item]\n[/item]\n[premium]\n[/premium]\n" +
			"[creature]\n100059 25 1 0 0 20 `test pet rename card` 0 0\n[/creature]\n" +
			"[coin]\n[/coin]\n[material]\n[/material]\n[recoveryitem]\n[/recoveryitem]\n",
		"monster/monster.lst":                     "",
		"equipment/equipment.lst":                 "",
		"stackable/stackable.lst":                 "25 `cash/creature/rename_card.stk`\n",
		"stackable/cash/creature/rename_card.stk": "[stackable type]\n`[creature]`\n",
	}
	catalog, err := newPVFCeraShopCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// newItemContainerTestCatalog 造一个最小目录：宠物更名卡（[creature]）、材料两个家族、
// 一个 [random reward item] 盒子（出奖 = 更名卡）。
func newItemContainerTestCatalog(t *testing.T) (*pvfDungeonDropCatalog, dungeonDropCatalogTestSource) {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "25 `cash/creature/rename_card.stk`\n" +
			"6515 `fp/fpcubepiece_clear.stk`\n" +
			"3277 `material/material_3277.stk`\n" +
			"4242 `ect/random_box.stk`\n",
		"stackable/cash/creature/rename_card.stk": "[name] `Rename Card`\n[stackable type] `[creature]`\n",
		"stackable/fp/fpcubepiece_clear.stk":      "[name] `Cube Piece`\n[stackable type] `[material instant item]`\n[stack limit] 1000\n",
		"stackable/material/material_3277.stk":    "[name] `Material`\n[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/ect/random_box.stk":            "[stackable type] `[random reward item]`\n[chn random image percent]\n0 99000 -1 `default.ani` ``\n1 1000 25 `rename.ani` ``\n[/chn random image percent]\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, source
}
