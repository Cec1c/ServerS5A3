package inventory

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// petFeedRewardItem 是运行时 PVF 里 resolveCurrentMagicBoxRewardItem 对宠物饲料
// （item 24 = stackable/cash/creature/creature_food.stk，[stackable type] [feed]）
// 给出的奖励投影：目标容器 list 7 的 189..238，StackLimit == 0（无上限，PVF 里
// 根本没有 [stack limit] 段）。
func petFeedRewardItem() alignedcmd.MagicBoxRewardItem {
	return alignedcmd.MagicBoxRewardItem{
		ItemID:         24,
		Kind:           "stackable",
		TargetListType: listTypePet,
		SlotStart:      189,
		SlotEnd:        238,
		PVFPath:        "stackable/cash/creature/creature_food.stk",
	}
}

func petFeedExistingRow(count int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID:   24,
		Count:    count,
		RawEntry: make([]byte, currentItemListEntrySize),
		Extra: map[string]string{
			"item_kind":                "stackable",
			"pvf_path":                 "stackable/cash/creature/creature_food.stk",
			"stackable_type":           "[feed]",
			"source":                   "cera_package",
			"last_grant_source":        "cera_package",
			"cera_package_source_item": "490701976",
		},
	}
}

// 开出宠物饲料必须并进已有的同身份 7:189 行（Extra 只差发放记账键 + 缓存 raw 行
// 镜像时也算同一行），不能另开一格、更不能落进主背包。
func TestGrantRandomRewardItemMergesPetFeedIntoExistingList7Row(t *testing.T) {
	items := map[string]dnfrepo.ItemStack{"7:189": petFeedExistingRow(600)}
	changed, err := grantRandomRewardItem(items, petFeedRewardItem(), 1)
	if err != nil {
		t.Fatalf("grant random reward: %v", err)
	}
	if len(changed) != 1 || changed[0] != 189 {
		t.Fatalf("changed slots=%v, want [189]", changed)
	}
	if len(items) != 1 {
		t.Fatalf("reward opened an extra row instead of merging: %+v", items)
	}
	if got := items["7:189"].Count; got != 601 {
		t.Fatalf("merged count=%d, want 601", got)
	}
	// 合并不能改写既有行的记账键。
	if extra := items["7:189"].Extra; extra["source"] != "cera_package" || extra["cera_package_source_item"] != "490701976" {
		t.Fatalf("merge rewrote the existing row's bookkeeping: %+v", extra)
	}
}

// 没有同身份行时在 list 7 的 PVF 段里开新格（主背包一格都不能占）。
func TestGrantRandomRewardItemPlacesPetFeedIntoPetConsumableSegment(t *testing.T) {
	items := map[string]dnfrepo.ItemStack{"0:65": {ItemID: 490701730, Count: 1}}
	changed, err := grantRandomRewardItem(items, petFeedRewardItem(), 2)
	if err != nil {
		t.Fatalf("grant random reward: %v", err)
	}
	if len(changed) != 1 || changed[0] != 189 {
		t.Fatalf("changed slots=%v, want [189]", changed)
	}
	created, found := items["7:189"]
	if !found || created.ItemID != 24 || created.Count != 2 {
		t.Fatalf("list-7 row=%+v found=%t", created, found)
	}
	if main := items["0:65"]; main.ItemID != 490701730 || main.Count != 1 {
		t.Fatalf("main inventory row was touched: %+v", main)
	}
}

// 身份冲突（同 item、不同 PVF 路径）不得合并：另开一格。
func TestGrantRandomRewardItemRefusesIdentityConflictInList7(t *testing.T) {
	conflicting := petFeedExistingRow(5)
	conflicting.Extra["pvf_path"] = "stackable/cash/creature/rename_card.stk"
	conflicting.Extra["stackable_type"] = "[creature]"
	items := map[string]dnfrepo.ItemStack{"7:189": conflicting}
	changed, err := grantRandomRewardItem(items, petFeedRewardItem(), 3)
	if err != nil {
		t.Fatalf("grant random reward: %v", err)
	}
	if got := items["7:189"].Count; got != 5 {
		t.Fatalf("conflicting row absorbed the reward (count=%d, want 5)", got)
	}
	created, found := items["7:190"]
	if !found || created.ItemID != 24 || created.Count != 3 {
		t.Fatalf("expected a fresh row 7:190 with count 3, slots=%+v (changed=%v)", items, changed)
	}
}

// 段位塞满时给出明确的"装不下"错误，而不是把奖励塞进别的容器。
func TestGrantRandomRewardItemReportsFullPetConsumableSegment(t *testing.T) {
	occupant := petFeedExistingRow(5)
	occupant.Extra["pvf_path"] = "stackable/cash/creature/rename_card.stk"
	items := map[string]dnfrepo.ItemStack{"7:189": occupant}
	reward := petFeedRewardItem()
	reward.SlotStart, reward.SlotEnd = 189, 189
	_, err := grantRandomRewardItem(items, reward, 1)
	if !errors.Is(err, ErrRandomRewardInventoryFull) {
		t.Fatalf("error=%v, want ErrRandomRewardInventoryFull", err)
	}
	if got := items["7:189"].Count; got != 5 {
		t.Fatalf("full-segment case mutated the foreign row: %d", got)
	}
}

// 不支持的容器仍然拒绝（契约保持显式，不静默落到主背包）。
func TestGrantRandomRewardItemStillRejectsUnsupportedContainer(t *testing.T) {
	reward := petFeedRewardItem()
	reward.TargetListType = 2
	if _, err := grantRandomRewardItem(map[string]dnfrepo.ItemStack{}, reward, 1); !errors.Is(err, ErrRandomRewardContractRequired) {
		t.Fatalf("error=%v, want ErrRandomRewardContractRequired", err)
	}
}

// 端到端：op44/op251 盒子开出宠物饲料时，整笔事务成功、奖励并进 7:189，
// 并且结果里如实带上"奖励在 list 7"（handler 据此刷新宠物背包）。
func TestOwnerUseStackableRandomRewardGrantsPetFeedIntoList7(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "79",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 65): {ItemID: 490701730, Count: 2},
			slotKey(listTypePet, 189): petFeedExistingRow(600),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.UseStackable(ctx, NewUseStackableCommand(alignedcmd.Request{
		AccountID:           "acc",
		SelectedCharacterID: 79,
	}, UseStackableRequest{
		SlotIndex:     65,
		ListType:      listTypeMain,
		InstanceValue: 0x11223344,
		ItemCode:      490701730,
	}), nil, petFeedRandomRewardResolver(490701730))
	if err != nil {
		t.Fatalf("UseStackable error = %v", err)
	}
	if result.RandomRewardItemID != 24 || result.RandomRewardListType != listTypePet ||
		len(result.RandomRewardSlots) != 1 || result.RandomRewardSlots[0] != 189 {
		t.Fatalf("result = %+v", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "79")
	if source := loaded.Slots[slotKey(listTypeMain, 65)]; source.ItemID != 490701730 || source.Count != 1 {
		t.Fatalf("source = %+v", source)
	}
	if feed := loaded.Slots[slotKey(listTypePet, 189)]; feed.ItemID != 24 || feed.Count != 601 {
		t.Fatalf("pet feed row = %+v", feed)
	}
	if _, found := loaded.Slots[slotKey(listTypePet, 190)]; found {
		t.Fatalf("pet feed was split into a second row: %+v", loaded.Slots)
	}
}

func petFeedRandomRewardResolver(sourceID int64) alignedcmd.RandomRewardItemResolver {
	return func(candidate int64) (alignedcmd.RandomRewardItemResolution, error) {
		if candidate != sourceID {
			return alignedcmd.RandomRewardItemResolution{}, nil
		}
		return alignedcmd.RandomRewardItemResolution{
			SourceItemID:  sourceID,
			SourcePVFPath: "stackable/cash/chn_490701730.stk",
			StackableType: "random reward item",
			Outcomes: []alignedcmd.RandomRewardItemOutcome{{
				Weight: 1,
				Reward: petFeedRewardItem(),
			}},
		}, nil
	}
}
