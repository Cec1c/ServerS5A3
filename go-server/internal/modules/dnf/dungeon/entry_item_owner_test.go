package dungeon

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// ── `[required item]` 入场消耗（收尾接线批）────────────────────────────────────
//
// 厂商对拍：
//
//	`Tool\PvfLib\Models\DungeonFile.cs:921-958`  解析 `[required item] ItemId Count Flag`，
//	   `:938 consumeOnEntry = values[index+2] == 1`。
//	`Network\Handlers\Dungeon\DungeonEntryAdmissionApplicationService.cs:170-183`
//	   以 `tournamentDefinition.EntryItems` 收取，**在入场准入里**走统一背包入口扣除。
//	`Docs\副本架构业务接入规范.md:862`
//	   "入场物品按 Definition 在进图时通过统一背包入口扣除并持久化…
//	    回城、失败或放弃不返还。"
//
// 真档已知数据（`_vendor-re\autotest\大会族-C批-照厂商实现-交付.md` 第 3.1 节）：
//
//	120 dungeon/shonantournament/yellowdragon.dgn → `[required item] 4726 1 1`
//	121 dungeon/shonantournament/bluedragon.dgn  → `[required item] 4727 1 1`
//
// 注意别与 `quest_4726.stk` 的 `[need material] 3332 5 3035 40`（**资格获取**的商店兑换）
// 混为一谈：那是另一个 owner 的定价段，不走这里。

func seedEntryItemInventory(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       slots,
	}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	return repos, owner
}

// 仓库存量足够 ⇒ 恰好扣掉 `Count`，不扣多、不扣 0；单格扣空时删除该格。
func TestConsumeEntryItemsTakesExactlyTheDeclaredCount(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:3": {ItemID: 4726, Count: 5},
	})
	result, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
		},
		UpdatedAt: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("ConsumeEntryItems: %v", err)
	}
	if !result.Changed || len(result.Consumed) != 1 {
		t.Fatalf("result = %+v", result)
	}
	consumed := result.Consumed[0]
	if consumed.ItemID != 4726 || consumed.Count != 1 || len(consumed.Slots) != 1 {
		t.Fatalf("consumed = %+v", consumed)
	}
	if consumed.Slots[0].Slot != 3 || consumed.Slots[0].CountAfter != 4 || consumed.Slots[0].Removed {
		t.Fatalf("slot = %+v", consumed.Slots[0])
	}
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:3"].Count != 4 {
		t.Fatalf("persisted slot count = %d want 4 (证物必须真的少一个，不是只校验)",
			inventory.Slots["0:3"].Count)
	}
}

// 持仓刚好等于要求 ⇒ 整格删掉，不留下 Count=0 的幽灵行。
func TestConsumeEntryItemsRemovesDepletedSlot(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:7": {ItemID: 4727, Count: 1},
	})
	result, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4727, Count: 1, ConsumeOnEntry: true},
		},
	})
	if err != nil {
		t.Fatalf("ConsumeEntryItems: %v", err)
	}
	if !result.Consumed[0].Slots[0].Removed || result.Consumed[0].Slots[0].CountAfter != 0 {
		t.Fatalf("slot = %+v want removed", result.Consumed[0].Slots[0])
	}
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if _, stillThere := inventory.Slots["0:7"]; stillThere {
		t.Fatalf("depleted slot survived: %+v", inventory.Slots)
	}
}

// 数量可以跨多个槽位凑（与 `ConsumeReviveCoin` 同一策略：小槽位优先）。
func TestConsumeEntryItemsSpansMultipleSlotsLowestFirst(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 4726, Count: 1},
		"0:2": {ItemID: 4726, Count: 2},
		"0:5": {ItemID: 4726, Count: 9},
	})
	result, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 4, ConsumeOnEntry: true},
		},
	})
	if err != nil {
		t.Fatalf("ConsumeEntryItems: %v", err)
	}
	slots := result.Consumed[0].Slots
	if len(slots) != 2 {
		t.Fatalf("slot count = %d want 2 (%+v)", len(slots), slots)
	}
	wantSlots := []int16{2, 5}
	for index, want := range wantSlots {
		if slots[index].Slot != want {
			t.Fatalf("slot order = %+v want 2 then 5 (lowest first)", slots)
		}
	}
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	// 合计扣掉 4：槽2 的 2 全扣 ⇒ 删；槽5 再扣 2 ⇒ 剩 7；槽9 未被动过。
	if _, gone := inventory.Slots["0:2"]; gone {
		t.Fatalf("slot 2 should be depleted: %+v", inventory.Slots)
	}
	if inventory.Slots["0:5"].Count != 7 || inventory.Slots["0:9"].Count != 1 {
		t.Fatalf("inventory = %+v want 0:5=7 0:9=1", inventory.Slots)
	}
}

// 持仓不足 ⇒ **硬失败**且整事务回滚（不能出现"扣了第一条、第二条不够"的半成品）。
func TestConsumeEntryItemsInsufficientRollsBackEveryRequirement(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:0": {ItemID: 4726, Count: 1},
		"0:1": {ItemID: 4727, Count: 0},
	})
	_, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
			{ItemID: 4727, Count: 1, ConsumeOnEntry: true},
		},
	})
	if !errors.Is(err, ErrEntryItemConsumeInsufficient) {
		t.Fatalf("err = %v want ErrEntryItemConsumeInsufficient", err)
	}
	inventory, _, loadErr := repos.Inventory.Load(ctx, "19")
	if loadErr != nil {
		t.Fatalf("load inventory: %v", loadErr)
	}
	if inventory.Slots["0:0"].Count != 1 {
		t.Fatalf("first requirement was not rolled back: %+v", inventory.Slots)
	}
}

// 完全没有该道具 ⇒ 也是不足（**绝不静默扣 0**）。
func TestConsumeEntryItemsMissingItemIsInsufficientNotZero(t *testing.T) {
	ctx := context.Background()
	_, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:0": {ItemID: 9999, Count: 100},
	})
	_, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
		},
	})
	if !errors.Is(err, ErrEntryItemConsumeInsufficient) {
		t.Fatalf("err = %v want ErrEntryItemConsumeInsufficient", err)
	}
}

// `ConsumeOnEntry == false` / 非法三元组 ⇒ 不参与扣减：不写库、不报错。
// 这条同时钉住"绝大多数副本（没有 `[required item]`）行为完全不变"。
func TestConsumeEntryItemsIgnoresNonConsumingRequirements(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:0": {ItemID: 4726, Count: 2},
	})
	result, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: false},
			{ItemID: 0, Count: 1, ConsumeOnEntry: true},
			{ItemID: 4726, Count: 0, ConsumeOnEntry: true},
			{ItemID: 4726, Count: -3, ConsumeOnEntry: true},
		},
	})
	if err != nil {
		t.Fatalf("ConsumeEntryItems: %v", err)
	}
	if result.Changed || len(result.Consumed) != 0 {
		t.Fatalf("result = %+v want an untouched inventory", result)
	}
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if inventory.Slots["0:0"].Count != 2 {
		t.Fatalf("non-consuming requirement mutated the inventory: %+v", inventory.Slots)
	}
}

// 非主背包列表（仓库 / 账号库）的格子不参与入场消耗 —— 与准入的持仓口径一致
// （`dnfbridge.currentCharacterItemCounts` 只数 `record.Slots` 的主背包键）。
func TestConsumeEntryItemsIgnoresNonMainListSlots(t *testing.T) {
	ctx := context.Background()
	_, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"7:0": {ItemID: 4726, Count: 10},
		"1:0": {ItemID: 4726, Count: 10},
	})
	_, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
		},
	})
	if !errors.Is(err, ErrEntryItemConsumeInsufficient) {
		t.Fatalf("err = %v want ErrEntryItemConsumeInsufficient (只有主背包列表参与)", err)
	}
}

// 投影回调只对"扣完还有剩余"的槽位调用一次，且投影结果真的落库。
func TestConsumeEntryItemsProjectsRemainingStacks(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:4": {ItemID: 4726, Count: 2},
	})
	projected := 0
	result, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
		},
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			projected++
			if slot != 4 {
				t.Fatalf("projected slot = %d want 4", slot)
			}
			if stack.Extra == nil {
				stack.Extra = make(map[string]string, 1)
			}
			stack.Extra["projected"] = "yes"
			return stack, nil
		},
	})
	if err != nil {
		t.Fatalf("ConsumeEntryItems: %v", err)
	}
	if projected != 1 || len(result.Consumed) != 1 {
		t.Fatalf("projected=%d result=%+v", projected, result)
	}
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	stack := inventory.Slots["0:4"]
	if stack.Count != 1 || stack.Extra["projected"] != "yes" {
		t.Fatalf("projected stack = %+v", stack)
	}
	if stack.Extra["last_consume_source"] != "dungeon_entry_required_item" {
		t.Fatalf("consume source marker = %q", stack.Extra["last_consume_source"])
	}
}

// 投影回调报错 ⇒ 整事务回滚（不会留下"已扣但没投影"的槽位）。
func TestConsumeEntryItemsProjectorFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	repos, owner := seedEntryItemInventory(t, map[string]dnfrepo.ItemStack{
		"0:4": {ItemID: 4726, Count: 2},
	})
	sentinel := errors.New("projector exploded")
	_, err := owner.ConsumeEntryItems(ctx, EntryItemConsumeCommand{
		CharacterID: "19",
		Requirements: []EntryItemRequirement{
			{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
		},
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			return dnfrepo.ItemStack{}, sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v want the projector sentinel", err)
	}
	inventory, _, loadErr := repos.Inventory.Load(ctx, "19")
	if loadErr != nil {
		t.Fatalf("load inventory: %v", loadErr)
	}
	if inventory.Slots["0:4"].Count != 2 {
		t.Fatalf("projector failure did not roll back: %+v", inventory.Slots)
	}
}
