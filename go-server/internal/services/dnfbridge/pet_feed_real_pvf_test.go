package dnfbridge

import (
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实 Script.pvf 回归：宠物饲料（item 24）在 list 7 的 189..238，且 PVF 里
// **没有 [stack limit] 段**（StackLimit == 0 = 无上限）。两次发放必须并进同一行——
// 这正是 2026-09-24 报障"同一件饲料被拆成多行"的根因：旧代码把合并循环整段放在
// `if definition.StackLimit > 0` 里，对猪料/更名卡这种"PVF 没写上限"的道具永远不合并。
func TestRealPVFPetFeedWithoutStackLimitMergesIntoPetConsumableSegment(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the pet-feed merge on the runtime PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(24)
	if err != nil {
		t.Fatalf("resolve pet feed item 24: %v", err)
	}
	if normalizeDungeonDropStackableType(definition.StackableType) != "[feed]" {
		t.Fatalf("item 24 stackable type=%q, want [feed]", definition.StackableType)
	}
	if definition.StackLimit != 0 {
		t.Fatalf("runtime creature_food.stk declares stack_limit=%d; if the PVF gained a real limit this test must be re-derived", definition.StackLimit)
	}

	// 盒子里开出宠物饲料时给出的奖励投影：list 7 的宠物消耗品段。
	reward, err := resolveCurrentMagicBoxRewardItem(catalog, archive, 24)
	if err != nil {
		t.Fatalf("resolve pet feed reward: %v", err)
	}
	if reward.TargetListType != currentPetInventoryListType ||
		reward.SlotStart != currentCeraShopPetConsumableSlotStart ||
		reward.SlotEnd != currentCeraShopPetConsumableSlotEnd ||
		reward.StackLimit != 0 {
		t.Fatalf("pet feed reward=%+v, want list 7 slots %d..%d with no stack limit",
			reward, currentCeraShopPetConsumableSlotStart, currentCeraShopPetConsumableSlotEnd)
	}

	inventory := &dnfrepo.InventoryRecord{
		CharacterID: "3",
		Slots: map[string]dnfrepo.ItemStack{
			"7:189": {
				ItemID: 24,
				Count:  300,
				Extra: map[string]string{
					"item_kind":                "stackable",
					"pvf_path":                 definition.PVFPath,
					"stackable_type":           definition.StackableType,
					"source":                   "cera_package",
					"last_grant_source":        "cera_package",
					"cera_package_source_item": "490701976",
				},
			},
		},
	}
	slots, err := grantCurrentCeraShopPetConsumable(inventory, definition, 300)
	if err != nil {
		t.Fatalf("grant pet feed: %v", err)
	}
	if len(inventory.Slots) != 1 || len(slots) != 1 || slots[0] != 189 {
		t.Fatalf("second grant must merge into 7:189, slots=%v inventory=%+v", slots, inventory.Slots)
	}
	if got := inventory.Slots["7:189"].Count; got != 600 {
		t.Fatalf("merged pet feed count=%d, want 600", got)
	}
}
