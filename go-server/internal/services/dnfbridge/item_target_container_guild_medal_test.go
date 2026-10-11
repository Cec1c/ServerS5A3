package dnfbridge

import (
	"os"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报障回归（2026-09-26，装备栏/守护珠两条）：
//
//	2. 「公会勋章获得时直接进入装备栏 无法被拖拽进入勋章栏-勋章 右键可直接装备」
//	3. 「公会守护珠获得时直接进入消耗品栏且拖拽装备无反应 无法被拖拽进入勋章栏-守护珠」
//
// 真因（发放侧）：容器判定 `currentPVFItemTargetContainer` 里**根本没有这两个家族**——
//   - 勋章 = `[equipment type] `[flag]`` ⇒ 落到 equipment 兜底 = 主背包装备页 9..64；
//   - 守护珠 = `[stackable type] `[flag gem]`` ⇒ `currentPVFMainStackableSegment` 无匹配
//     ⇒ 落到默认消耗品页 65..120。
//
// wire 容器真源 = list 38，且它是**两个 49 格分页拼成的 98 格容器**：
// 勋章 0..48、守护珠 49..97（`currentGuildMedalInventoryListType` /
// `currentGuildMedalActorSlot` = actor 槽 32 的穿戴端点在 `equipment_placement.go` 与
// `guardian_gem_protocol.go` 里都已经是 38；A21 参考实现
// `ItemSlotBoundService.TryGetSlotRange` 同样是 GuildMedal 0..48 / GuardianGem 49..97）。

const (
	guildMedalRoutingItemID  int64 = 100380017 // equipment/flag/100380017.equ, [flag]
	guardianGemRoutingItemID int64 = 90002     // stackable/flaggem/physical_defense_grade2.stk, [flag gem]
)

func guildMedalRoutingCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":          "",
		"equipment/equipment.lst":      "100380017 `flag/100380017.equ`\n35381 `weapon/35381.equ`\n",
		"stackable/stackable.lst":      "90002 `flaggem/physical_defense_grade2.stk`\n25 `cash/creature/rename_card.stk`\n",
		"equipment/flag/100380017.equ": "[equipment type]\n`[flag]`\n",
		"equipment/weapon/35381.equ":   "[equipment type]\n`[weapon]`\n",
		"stackable/flaggem/physical_defense_grade2.stk": "[stackable type]\n`[flag gem]`\n[grade]\n2\n" +
			"[enchant]\n[equipment physical defense]\n100\n[/enchant]\n",
		"stackable/cash/creature/rename_card.stk": "[stackable type]\n`[creature]`\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// 家族→容器判定：勋章进 list 38 的 0..48，守护珠进 list 38 的 49..97。
func TestCurrentPVFItemTargetContainerRoutesGuildMedalAndGuardianGemToList38(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)

	medal, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatalf("resolve medal: %v", err)
	}
	if medal.Kind != dungeonDropItemEquipment || normalizeEquipmentPlacementPVFType(medal.EquipmentType) != "[flag]" {
		t.Fatalf("medal definition = %+v (装备家族必须是 [flag])", medal)
	}
	medalContainer := currentPVFItemTargetContainer(medal)
	if medalContainer.ListType != currentGuildMedalInventoryListType ||
		medalContainer.SlotStart != currentGuildMedalPageSlotStart ||
		medalContainer.SlotEnd != currentGuildMedalPageSlotEnd ||
		!medalContainer.GuildMedal || medalContainer.GuardianGem {
		t.Fatalf("medal container = %+v, want list=%d slots=%d..%d GuildMedal",
			medalContainer, currentGuildMedalInventoryListType,
			currentGuildMedalPageSlotStart, currentGuildMedalPageSlotEnd)
	}

	gem, err := catalog.ResolveItem(uint32(guardianGemRoutingItemID))
	if err != nil {
		t.Fatalf("resolve guardian gem: %v", err)
	}
	if gem.Kind != dungeonDropItemStackable || !currentGuardianGemStackableType(normalizeDungeonDropStackableType(gem.StackableType)) {
		t.Fatalf("gem definition = %+v (堆叠家族必须是 [flag gem])", gem)
	}
	gemContainer := currentPVFItemTargetContainer(gem)
	if gemContainer.ListType != currentGuildMedalInventoryListType ||
		gemContainer.SlotStart != currentGuardianGemPageSlotStart ||
		gemContainer.SlotEnd != currentGuardianGemPageSlotEnd ||
		!gemContainer.GuardianGem || gemContainer.GuildMedal {
		t.Fatalf("gem container = %+v, want list=%d slots=%d..%d GuardianGem",
			gemContainer, currentGuildMedalInventoryListType,
			currentGuardianGemPageSlotStart, currentGuardianGemPageSlotEnd)
	}

	// 两个分页必须严格相邻且不重叠：勋章 0..48 + 守护珠 49..97 = 98 格。
	if currentGuildMedalPageSlotEnd+1 != currentGuardianGemPageSlotStart {
		t.Fatalf("guild-medal pages are not adjacent: %d..%d then %d..%d",
			currentGuildMedalPageSlotStart, currentGuildMedalPageSlotEnd,
			currentGuardianGemPageSlotStart, currentGuardianGemPageSlotEnd)
	}

	// 未被本次改动碰到的家族必须逐值不变（普通装备仍进主背包装备页）。
	weapon, err := catalog.ResolveItem(35381)
	if err != nil {
		t.Fatalf("resolve weapon: %v", err)
	}
	if container := currentPVFItemTargetContainer(weapon); container.ListType != dnfrepo.MainInventoryListType ||
		container.SlotStart != currentPVFMainEquipmentSlotStart || container.SlotEnd != currentPVFMainEquipmentSlotEnd {
		t.Fatalf("weapon container = %+v, want main 9..64", container)
	}
	renameCard, err := catalog.ResolveItem(25)
	if err != nil {
		t.Fatalf("resolve rename card: %v", err)
	}
	if container := currentPVFItemTargetContainer(renameCard); !container.PetConsumable {
		t.Fatalf("rename card container = %+v, want pet consumable", container)
	}
}

// 邮件领取（GM 面板发放也走这一条）必须把勋章/守护珠落进 list 38 的对应分页。
func TestMailboxResolutionRoutesGuildMedalAndGuardianGemToList38(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)

	medal, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	medalResolution, err := mailboxItemResolutionFromPVF(medal, false)
	if err != nil {
		t.Fatalf("medal mailbox resolution: %v", err)
	}
	if medalResolution.TargetListType != currentGuildMedalInventoryListType ||
		medalResolution.SlotStart != currentGuildMedalPageSlotStart ||
		medalResolution.SlotEnd != currentGuildMedalPageSlotEnd ||
		medalResolution.StackLimit != 1 {
		t.Fatalf("medal mailbox resolution = %+v", medalResolution)
	}

	gem, err := catalog.ResolveItem(uint32(guardianGemRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	gemResolution, err := mailboxItemResolutionFromPVF(gem, false)
	if err != nil {
		t.Fatalf("gem mailbox resolution: %v", err)
	}
	if gemResolution.TargetListType != currentGuildMedalInventoryListType ||
		gemResolution.SlotStart != currentGuardianGemPageSlotStart ||
		gemResolution.SlotEnd != currentGuardianGemPageSlotEnd {
		t.Fatalf("gem mailbox resolution = %+v", gemResolution)
	}
}

// 发放落库：勋章落 38:0 且每格 1 件，守护珠并进 38:49 的同身份行（StackLimit 0 = 无上限）。
func TestGrantCurrentGuildMedalPageItemLandsInList38Pages(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)

	medal, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{CharacterID: "77"}
	slots, err := grantCurrentGuildMedalPageItem(&inventory, medal, currentPVFItemTargetContainer(medal), 2)
	if err != nil {
		t.Fatalf("grant medal: %v", err)
	}
	if !reflect.DeepEqual(slots, []int16{0, 1}) {
		t.Fatalf("medal slots = %v, want [0 1]", slots)
	}
	for _, slot := range []int16{0, 1} {
		stack, found := inventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slot)]
		if !found || stack.ItemID != guildMedalRoutingItemID || stack.Count != 1 {
			t.Fatalf("medal row 38:%d = %+v found=%t", slot, stack, found)
		}
		if len(stack.RawEntry) != currentItemListEntryWireSize {
			t.Fatalf("medal row 38:%d raw entry length=%d", slot, len(stack.RawEntry))
		}
	}
	if _, leaked := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, currentPVFMainEquipmentSlotStart)]; leaked {
		t.Fatalf("medal leaked into the main equipment page: %+v", inventory.Slots)
	}

	gem, err := catalog.ResolveItem(uint32(guardianGemRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	gemInventory := dnfrepo.InventoryRecord{CharacterID: "77"}
	gemSlots, err := grantCurrentGuildMedalPageItem(&gemInventory, gem, currentPVFItemTargetContainer(gem), 3)
	if err != nil {
		t.Fatalf("grant gem: %v", err)
	}
	if !reflect.DeepEqual(gemSlots, []int16{currentGuardianGemPageSlotStart}) {
		t.Fatalf("gem slots = %v, want [%d]", gemSlots, currentGuardianGemPageSlotStart)
	}
	gemStack := gemInventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, currentGuardianGemPageSlotStart)]
	if gemStack.ItemID != guardianGemRoutingItemID || gemStack.Count != 3 {
		t.Fatalf("gem row = %+v", gemStack)
	}
	// 第二批同身份守护珠必须并进 38:49，而不是另开一格（"prefer stacking over new slot"）。
	moreSlots, err := grantCurrentGuildMedalPageItem(&gemInventory, gem, currentPVFItemTargetContainer(gem), 2)
	if err != nil {
		t.Fatalf("grant gem again: %v", err)
	}
	if !reflect.DeepEqual(moreSlots, []int16{currentGuardianGemPageSlotStart}) {
		t.Fatalf("second gem grant slots = %v, want merge into %d", moreSlots, currentGuardianGemPageSlotStart)
	}
	if merged := gemInventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, currentGuardianGemPageSlotStart)]; merged.Count != 5 {
		t.Fatalf("gem stack after merge = %+v, want count=5", merged)
	}
	// 守护珠绝不能落进主背包消耗品页（现场报障"直接进入消耗品栏"）。
	for slot := currentPVFMainConsumableStart; slot <= currentPVFMainConsumableEnd; slot++ {
		if _, leaked := gemInventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)]; leaked {
			t.Fatalf("guardian gem leaked into the main consumable page at 0:%d", slot)
		}
	}
}

// 分页占满时必须失败关闭（绝不溢出到另一页或主背包）。
func TestGrantCurrentGuildMedalPageItemFailsClosedWhenPageIsFull(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)
	gem, err := catalog.ResolveItem(uint32(guardianGemRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{},
	}
	for slot := currentGuardianGemPageSlotStart; slot <= currentGuardianGemPageSlotEnd; slot++ {
		inventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slot)] =
			dnfrepo.ItemStack{ItemID: 700000 + int64(slot), Count: 1}
	}
	before := len(inventory.Slots)
	if _, err := grantCurrentGuildMedalPageItem(&inventory, gem, currentPVFItemTargetContainer(gem), 1); err == nil {
		t.Fatal("a full guardian-gem page must fail closed")
	}
	if len(inventory.Slots) != before {
		t.Fatalf("failed grant mutated the inventory: %d -> %d rows", before, len(inventory.Slots))
	}
	if _, inMedalPage := inventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, currentGuildMedalPageSlotStart)]; inMedalPage {
		t.Fatal("failed guardian-gem grant spilled into the medal page")
	}
}

// 礼盒（booster）投放：勋章/守护珠必须走 list 38 的 placement，而不是主背包兜底。
func TestCurrentBoosterPlacementRoutesGuildMedalAndGuardianGemToList38(t *testing.T) {
	catalog := guildMedalRoutingCatalog(t)

	medal, err := catalog.ResolveItem(uint32(guildMedalRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	if placement, err := currentBoosterPlacement(medal); err != nil || placement != currentBoosterRewardGuildMedal {
		t.Fatalf("medal placement = %v err=%v, want guild medal", placement, err)
	}
	gem, err := catalog.ResolveItem(uint32(guardianGemRoutingItemID))
	if err != nil {
		t.Fatal(err)
	}
	if placement, err := currentBoosterPlacement(gem); err != nil || placement != currentBoosterRewardGuildMedal {
		t.Fatalf("gem placement = %v err=%v, want guild medal", placement, err)
	}
}

// 真实运行时 PVF 回归（DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf>）：
// 真档里的勋章/守护珠家族必须真的落进 list 38 的两个分页，而不是装备页/消耗品页。
func TestRealPVFGuildMedalAndGuardianGemRouteToList38(t *testing.T) {
	archive := openGuildMedalRealPVFSmokeArchive(t)
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	medals, gems := 0, 0
	for itemID, reference := range catalog.itemRefs {
		path := strings.ToLower(reference.path)
		switch reference.kind {
		case dungeonDropItemEquipment:
			if !strings.Contains(path, "flag") {
				continue
			}
			definition, err := catalog.ResolveItem(itemID)
			if err != nil {
				continue
			}
			container := currentPVFItemTargetContainer(definition)
			if !container.GuildMedal {
				// equipment/flag/ 下也可能有非 [flag] 家族；只对 [flag] 断言。
				if normalizeEquipmentPlacementPVFType(definition.EquipmentType) == currentGuildMedalPVFType {
					t.Fatalf("real [flag] item=%d path=%q container=%+v", itemID, path, container)
				}
				continue
			}
			medals++
			if container.ListType != currentGuildMedalInventoryListType ||
				container.SlotStart != currentGuildMedalPageSlotStart ||
				container.SlotEnd != currentGuildMedalPageSlotEnd {
				t.Fatalf("real medal item=%d type=%q container=%+v", itemID, definition.EquipmentType, container)
			}
		case dungeonDropItemStackable:
			if !strings.Contains(path, "flaggem") {
				continue
			}
			definition, err := catalog.ResolveItem(itemID)
			if err != nil {
				continue
			}
			container := currentPVFItemTargetContainer(definition)
			if !container.GuardianGem {
				if currentGuardianGemStackableType(normalizeDungeonDropStackableType(definition.StackableType)) {
					t.Fatalf("real [flag gem] item=%d path=%q container=%+v", itemID, path, container)
				}
				continue
			}
			gems++
			if container.ListType != currentGuildMedalInventoryListType ||
				container.SlotStart != currentGuardianGemPageSlotStart ||
				container.SlotEnd != currentGuardianGemPageSlotEnd {
				t.Fatalf("real guardian gem item=%d type=%q container=%+v", itemID, definition.StackableType, container)
			}
		}
	}
	if medals == 0 || gems == 0 {
		t.Fatalf("runtime PVF yielded medals=%d guardian gems=%d; the family scan did not run as expected", medals, gems)
	}
	t.Logf("runtime PVF: %d guild medals -> list 38 0..48, %d guardian gems -> list 38 49..97", medals, gems)
}

// openGuildMedalRealPVFSmokeArchive 只在 DNFBRIDGE_REAL_PVF_SMOKE 指向运行时
// Script.pvf 时返回档案，否则整条用例跳过（与其它真档回归同一约定）。
func openGuildMedalRealPVFSmokeArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}
