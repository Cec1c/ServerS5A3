package inventory

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场回归：强化/增幅失败把装备摧毁时，补偿道具（运行时 PVF
// `etc/upgrade.etc [disjoint bonus item] 3243` = 强烈之痕迹、
// `etc/amplifyupgrade.etc [disjoint bonus item] 3244` = 浓缩的纯洁之骸）
// 必须并进同身份的既有行，而不是每摧毁一把武器就另开一格。
//
// 线上取证（2026-09-28，只读）：角色 6（test78）item 3243 四行
// 0:129 ×9 / 0:137 ×16 / 0:138 ×9 / 0:141 ×9，四行 Extra 都只有
// source=upgrade_destroy_bonus；角色 5 item 3244 三行 0:60/0:123/0:124。
//
// 这些用例断言的是**落库后的行**（loadTestInventory），不是标志位。

// destroyBonusLiveStack 复刻线上那一行的形状：item 3243、数量 9、
// Extra 只有 source=upgrade_destroy_bonus（没有 pvf_path/stackable_type，
// 正是"身份键缺省即兼容"能合并的前提）。
func destroyBonusLiveStack(itemID int64, count int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: itemID,
		Count:  count,
		Extra:  map[string]string{"source": "upgrade_destroy_bonus"},
	}
}

func destroyBonusEquipment(mode string) dnfrepo.ItemStack {
	extra := map[string]string{
		"item_kind":      "equipment",
		"durability":     "45",
		"max_durability": "45",
	}
	if mode == "amplify" {
		extra["amplify_type"] = "3"
		extra["amplify_value"] = "5"
	}
	return dnfrepo.ItemStack{ItemID: 700, Count: 1, Extra: extra}
}

// stacksForItem 返回某 item 在主背包里的所有行（按槽位排序），供"到底几行"的断言。
func stacksForItem(record dnfrepo.InventoryRecord, itemID int64) []int16 {
	slots := make([]int16, 0, 4)
	for key, stack := range record.Slots {
		listType, slot, ok := parseSlotKey(key)
		if !ok || listType != listTypeMain || stack.ItemID != itemID || stack.Count <= 0 {
			continue
		}
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return slots
}

type destroyBonusCase struct {
	mode       string
	bonusItem  int64
	bonusCount int
	identity   alignedcmd.StackGrantIdentity
	container  alignedcmd.StackGrantContainer
	slots      map[string]dnfrepo.ItemStack
}

// destroyBonusMaterialContainer 是运行时 PVF 给这两个补偿道具的落位段：
// `[material]` → list 0 121..176（dnfbridge/item_target_container.go 是唯一真源）。
// 现场 0:129/0:137/0:138/0:141/0:123/0:124 全在这段里，而 2026-09-28 那版把新格
// 落在 0:12/0:60 —— 装备页，就是操作者复报的"还是不能正常堆叠"。
func destroyBonusMaterialContainer() alignedcmd.StackGrantContainer {
	return alignedcmd.StackGrantContainer{ListType: listTypeMain, SlotStart: 121, SlotEnd: 176}
}

// destroyBonusPVFIdentity 是运行时 PVF 里那件补偿道具的真实身份（真实 PVF 取证见
// dnfbridge/upgrade_destroy_bonus_stack_test.go：目录给出的 pvf_path 带 `stackable/`
// 前缀；GM 索引把这段前缀裁掉了，两处指同一个文件）。
// stackLimit 0 = PVF 没写 [stack limit] = 无上限。
func destroyBonusPVFIdentity(itemID int64, stackLimit int64) alignedcmd.StackGrantIdentity {
	path := "stackable/material/vestiges_intensive.stk"
	if itemID == 3244 {
		path = "stackable/material/crystal_pure.stk"
	}
	return alignedcmd.StackGrantIdentity{
		StackLimit:    stackLimit,
		PVFPath:       path,
		ItemKind:      "stackable",
		StackableType: "[material]",
	}
}

// runDestroyBonusUpgrade 真的走 owner.Upgrade 的摧毁档，并把落库后的背包交回。
//
// RNG 用的是 math/rand 全局源（owner.go:1406 的 rand.Intn(currentUpgradeSuccessBase)），
// 而 Go 1.24 起 rand.Seed 已是 no-op，测试里没法种种子。这里用既有的 PVF 口径
// `UpgradeSuccessWeight = 1`（单次不摧毁的概率 1/100000）重试到摧毁为止：
// 连续 64 次都不摧毁的概率是 1e-320，实际上不可达；真发生就 t.Fatalf，绝不静默跳过。
func runDestroyBonusUpgrade(t *testing.T, fixture destroyBonusCase) (UpgradeResult, dnfrepo.InventoryRecord) {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 64; attempt++ {
		repos := dnfrepomemory.NewMemoryGroup()
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
			CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
		})
		slots := make(map[string]dnfrepo.ItemStack, len(fixture.slots)+1)
		for key, stack := range fixture.slots {
			slots[key] = cloneStack(stack)
		}
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots})
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatalf("NewOwner error = %v", err)
		}
		cmd := NewUpgradeCommand(alignedcmd.Request{
			AccountID:           "acc",
			SelectedCharacterID: 77,
		}, UpgradeItemRequest{
			Mode:                    fixture.mode,
			TargetSlotIndex:         9,
			TargetItemTemplateID:    700,
			MaterialSlotIndex:       -1,
			OptionalTicketSlotIndex: -1,
		})
		cmd.UpgradeSuccessWeight = 1
		cmd.UpgradePenaltyType = 3 // destroy
		cmd.UpgradeDestroyBonusItemID = int(fixture.bonusItem)
		cmd.UpgradeDestroyBonusCount = fixture.bonusCount
		cmd.UpgradeDestroyBonusIdentity = fixture.identity
		cmd.UpgradeDestroyBonusContainer = fixture.container
		result, err := owner.Upgrade(ctx, cmd)
		if err != nil {
			t.Fatalf("Upgrade error = %v", err)
		}
		if !result.Success {
			t.Fatalf("Upgrade not applied: %+v", result)
		}
		if result.ResultCode == upgradeResultCodeDestroy {
			return result, loadTestInventory(t, ctx, repos, "77")
		}
	}
	t.Fatalf("64 次都没落到摧毁档（successWeight=1 时该概率 < 1e-300）")
	return UpgradeResult{}, dnfrepo.InventoryRecord{}
}

// ① 报障场景：既有 0:129 ×9，再摧毁一把武器产出 7 个——必须并进 0:129（16），
// 不得再出现任何新行。
func TestOwnerUpgradeDestroyBonusMergesIntoExistingStack(t *testing.T) {
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): destroyBonusLiveStack(3243, 9),
		},
	})
	if got := stacksForItem(loaded, 3243); len(got) != 1 || got[0] != 129 {
		t.Fatalf("item 3243 rows = %v, want exactly [129] (inventory=%+v)", got, loaded.Slots)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Count; got != 16 {
		t.Fatalf("0:129 count = %d, want 16 (9+7)", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Extra["source"]; got != "upgrade_destroy_bonus" {
		t.Fatalf("merged row lost its bookkeeping key: source=%q", got)
	}
	if result.DestroyBonusSlot != 129 || !result.DestroyBonusMerged || result.DestroyBonusCreated {
		t.Fatalf("placement = %+v, want slot=129 merged=true created=false", result)
	}
	if result.DestroyBonusPlacedCount != 7 {
		t.Fatalf("placed = %d, want 7", result.DestroyBonusPlacedCount)
	}
	// 目标装备必须真的被摧毁（摧毁档的语义没被这次修复改变）。
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; ok {
		t.Fatalf("target slot 9 survived a destroy-penalty attempt")
	}
	// op14 增量必须包含真正改动的那一行（槽 9 是既有的"清空目标行"增量，
	// 与修复前逐字节相同，不在本次判据里）。
	entries := upgradeItemUpdateEntries(Command{}, result)
	if len(entries) != 2 || entries[0].slot != 9 || entries[1].slot != 129 {
		t.Fatalf("op14 entries = %+v, want the cleared target row (9) plus the merged row (129)", entries)
	}
	if entries[1].stack.ItemID != 3243 || entries[1].stack.Count != 16 {
		t.Fatalf("op14 merged row = %+v, want item 3243 count 16", entries[1].stack)
	}
}

// ② 没有同身份行时仍然要新开一格（修复不能把"该开格"也吃掉），而且新格必须落在
// **本道具的 PVF 家族段**（`[material]` → 121..176）里，Extra 必须写全堆叠契约键——
// 这两件事就是操作者"还是不能正常堆叠"的两层根因（错页 + 缺契约键 ⇒ op19 判成 swap）。
func TestOwnerUpgradeDestroyBonusOpensNewSlotWhenNoSameStack(t *testing.T) {
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): destroyBonusEquipment("reinforce"),
		},
	})
	if got := stacksForItem(loaded, 3243); len(got) != 1 {
		t.Fatalf("item 3243 rows = %v, want exactly one", got)
	}
	newRow := loaded.Slots[slotKey(listTypeMain, result.DestroyBonusSlot)]
	if newRow.ItemID != 3243 || newRow.Count != 7 {
		t.Fatalf("new slot %d = %+v, want item 3243 count 7", result.DestroyBonusSlot, newRow)
	}
	if result.DestroyBonusSlot != 121 {
		t.Fatalf("new slot = %d, want 121 (first empty slot of the `[material]` segment 121..176)", result.DestroyBonusSlot)
	}
	if result.DestroyBonusMerged || !result.DestroyBonusCreated {
		t.Fatalf("placement = %+v, want merged=false created=true", result)
	}
	wantExtra := map[string]string{
		"source":         "upgrade_destroy_bonus",
		"item_kind":      "stackable",
		"pvf_path":       "stackable/material/vestiges_intensive.stk",
		"stackable_type": "[material]",
	}
	if !reflect.DeepEqual(newRow.Extra, wantExtra) {
		t.Fatalf("new row Extra = %v, want %v (PVF stack contract, no stack_limit for an uncapped PVF)", newRow.Extra, wantExtra)
	}
}

// ③ PVF [stack limit] 必须封顶：既有行 995/1000 再给 7 ⇒ 既有行到 1000，
// 溢出 2 落在材料段的第一个空格 121；两行都在 op14 增量里。
func TestOwnerUpgradeDestroyBonusHonorsPVFStackLimitThenOpensSlot(t *testing.T) {
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 1000),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): destroyBonusLiveStack(3243, 995),
		},
	})
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Count; got != 1000 {
		t.Fatalf("0:129 count = %d, want 1000 (PVF stack limit)", got)
	}
	rows := stacksForItem(loaded, 3243)
	if len(rows) != 2 {
		t.Fatalf("item 3243 rows = %v, want [121(溢出) 129(封顶)]", rows)
	}
	spill := rows[0]
	if spill != 121 {
		t.Fatalf("spill slot = %d, want 121 (material segment start)", spill)
	}
	if got := loaded.Slots[slotKey(listTypeMain, spill)].Count; got != 2 {
		t.Fatalf("spill slot %d count = %d, want 2 (7 - 5 room)", spill, got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, spill)].Extra["stack_limit"]; got != "1000" {
		t.Fatalf("spill row stack_limit = %q, want 1000 (PVF [stack limit] must be carried)", got)
	}
	if !result.DestroyBonusMerged || !result.DestroyBonusCreated || result.DestroyBonusPlacedCount != 7 {
		t.Fatalf("placement = %+v, want merged=true created=true placed=7", result)
	}
	if result.DestroyBonusSlot != 129 {
		t.Fatalf("ACK slot = %d, want 129 (the merged row, not the spill)", result.DestroyBonusSlot)
	}
	// op14：清空的目标行 + 封顶的既有行 + 溢出新格。
	entries := upgradeItemUpdateEntries(Command{}, result)
	if len(entries) != 3 || entries[1].slot != 121 || entries[2].slot != 129 {
		t.Fatalf("op14 entries = %+v, want slots [9 121 129]", entries)
	}
}

// ④ 原始回退档字面量（first_empty_slot = 2026-09-28 之前那一版）逐字节复现：
// 不看既有行、直接另开一格（60）、新行 Extra 只有 source=upgrade_destroy_bonus。
func TestOwnerUpgradeDestroyBonusRollbackLiteralReproducesFirstEmptySlot(t *testing.T) {
	t.Setenv(UpgradeDestroyBonusPlacementEnv, UpgradeDestroyBonusPlacementFirstEmptySlot)
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): destroyBonusLiveStack(3243, 9),
		},
	})
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Count; got != 9 {
		t.Fatalf("rollback literal mutated the existing row: 0:129 = %d, want 9", got)
	}
	rows := stacksForItem(loaded, 3243)
	if len(rows) != 2 {
		t.Fatalf("rollback literal rows = %v, want 2 (existing + a fresh slot, i.e. today's split)", rows)
	}
	if result.DestroyBonusSlot != 60 || result.DestroyBonusMerged || !result.DestroyBonusCreated {
		t.Fatalf("rollback placement = %+v, want slot=60 merged=false created=true", result)
	}
	newRow := loaded.Slots[slotKey(listTypeMain, 60)]
	if newRow.Count != 7 {
		t.Fatalf("rollback new row count = %d, want 7", newRow.Count)
	}
	if !reflect.DeepEqual(newRow.Extra, map[string]string{"source": "upgrade_destroy_bonus"}) {
		t.Fatalf("rollback new row Extra = %v, want exactly {source:upgrade_destroy_bonus} (byte-for-byte pre-fix)", newRow.Extra)
	}
}

// ④b 中间回退档字面量（merge_existing_stack_legacy_row = 2026-09-28 已部署那一版）
// 逐字节复现：会并行，但新行不写契约键、新格仍落在 firstEmptyUpgradeSlot() 的 60。
// 这一档是给"本轮修复要 A/B 对比"用的，默认档不再是它。
func TestOwnerUpgradeDestroyBonusLegacyRowLiteralReproducesTheMidState(t *testing.T) {
	t.Setenv(UpgradeDestroyBonusPlacementEnv, UpgradeDestroyBonusPlacementLegacyRow)
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		// 上限 10：既有行 9 只能再吞 1 个，剩下 6 个必须走"新开格"这条路——
		// 这正是 2026-09-28 那一版的落点（firstEmptyUpgradeSlot → 60）。
		identity:  destroyBonusPVFIdentity(3243, 10),
		container: destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): destroyBonusLiveStack(3243, 9),
		},
	})
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Count; got != 10 {
		t.Fatalf("legacy-row literal must still merge up to the PVF limit: 0:129 = %d, want 10", got)
	}
	rows := stacksForItem(loaded, 3243)
	if len(rows) != 2 || rows[0] != 60 || rows[1] != 129 {
		t.Fatalf("legacy-row rows = %v, want [60(legacy new slot) 129(merged)]", rows)
	}
	if result.DestroyBonusSlot != 129 || !result.DestroyBonusMerged {
		t.Fatalf("legacy-row placement = %+v, want slot=129 merged=true", result)
	}
	// 反过来：在默认档下同一场发放必须留下契约键；legacy 档必须不写。
	if got := loaded.Slots[slotKey(listTypeMain, 60)].Extra; !reflect.DeepEqual(got, map[string]string{"source": "upgrade_destroy_bonus"}) {
		t.Fatalf("legacy-row new row Extra = %v, want exactly {source:upgrade_destroy_bonus}", got)
	}
	if _, _, ok := dnfrepo.StackContract(loaded.Slots[slotKey(listTypeMain, 60)]); ok {
		t.Fatalf("legacy-row new row unexpectedly satisfies the stack contract (this literal must reproduce the old behaviour)")
	}
}

// ⑤ 第二个路由（增幅档，`etc/amplifyupgrade.etc [disjoint bonus item] 3244`，
// 现场角色 5 的三行就是它）必须与强化档同一条判据、同一处合并 ⇒ 这是类修复。
func TestOwnerUpgradeDestroyBonusAmplifyRouteMergesTheSameWay(t *testing.T) {
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "amplify",
		bonusItem:  3244,
		bonusCount: 4,
		identity:   destroyBonusPVFIdentity(3244, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("amplify"),
			slotKey(listTypeMain, 123): destroyBonusLiveStack(3244, 4),
		},
	})
	if got := stacksForItem(loaded, 3244); len(got) != 1 || got[0] != 123 {
		t.Fatalf("item 3244 rows = %v, want exactly [123]", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 123)].Count; got != 8 {
		t.Fatalf("0:123 count = %d, want 8 (4+4)", got)
	}
	if result.DestroyBonusSlot != 123 || !result.DestroyBonusMerged {
		t.Fatalf("amplify placement = %+v, want slot=123 merged=true", result)
	}
}

// ⑥ 操作者的验收路径：连着摧毁两把武器（第二次没有预先存在的行）——
// 第一次开材料段的第一个空格 121 ×7，第二次必须并进 121 变成 14，全程只有一行。
func TestOwnerUpgradeDestroyBonusConsecutiveDestroysKeepOneRow(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	slots := map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 9):  destroyBonusEquipment("reinforce"),
		slotKey(listTypeMain, 10): destroyBonusEquipment("reinforce"),
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	firstSlot := int16(-1)
	for _, targetSlot := range []int16{9, 10} {
		destroyed := false
		for attempt := 0; attempt < 64 && !destroyed; attempt++ {
			cmd := NewUpgradeCommand(alignedcmd.Request{
				AccountID:           "acc",
				SelectedCharacterID: 77,
			}, UpgradeItemRequest{
				Mode:                    "reinforce",
				TargetSlotIndex:         targetSlot,
				TargetItemTemplateID:    700,
				MaterialSlotIndex:       -1,
				OptionalTicketSlotIndex: -1,
			})
			cmd.UpgradeSuccessWeight = 1
			cmd.UpgradePenaltyType = 3
			cmd.UpgradeDestroyBonusItemID = 3243
			cmd.UpgradeDestroyBonusCount = 7
			cmd.UpgradeDestroyBonusIdentity = destroyBonusPVFIdentity(3243, 0)
			cmd.UpgradeDestroyBonusContainer = destroyBonusMaterialContainer()
			result, err := owner.Upgrade(ctx, cmd)
			if err != nil {
				t.Fatalf("Upgrade(%d) error = %v", targetSlot, err)
			}
			if result.ResultCode != upgradeResultCodeDestroy {
				continue
			}
			destroyed = true
			if firstSlot < 0 {
				firstSlot = result.DestroyBonusSlot
				if firstSlot != 121 {
					t.Fatalf("first destroy landed in slot %d, want the material-segment start 121", firstSlot)
				}
			} else if result.DestroyBonusSlot != firstSlot || !result.DestroyBonusMerged {
				t.Fatalf("second destroy landed in slot %d merged=%t, want slot=%d merged=true",
					result.DestroyBonusSlot, result.DestroyBonusMerged, firstSlot)
			}
		}
		if !destroyed {
			t.Fatalf("target slot %d never reached the destroy branch", targetSlot)
		}
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if rows := stacksForItem(loaded, 3243); len(rows) != 1 || rows[0] != firstSlot {
		t.Fatalf("item 3243 rows = %v, want exactly [%d] after two destroys", rows, firstSlot)
	}
	if got := loaded.Slots[slotKey(listTypeMain, firstSlot)].Count; got != 14 {
		t.Fatalf("row count = %d, want 14 (two 7-item rewards in one row)", got)
	}
}

// ⑦ 开关归一化：空 / 未知取值 = 默认档；大小写与空白不敏感；只有回退字面量才回退。
func TestUpgradeDestroyBonusPlacementModeNormalization(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", UpgradeDestroyBonusPlacementMerge},
		{"merge_existing_stack", UpgradeDestroyBonusPlacementMerge},
		{"  Merge_Existing_Stack  ", UpgradeDestroyBonusPlacementMerge},
		{"first_empty_slot", UpgradeDestroyBonusPlacementFirstEmptySlot},
		{" FIRST_EMPTY_SLOT ", UpgradeDestroyBonusPlacementFirstEmptySlot},
		{"merge_existing_stack_legacy_row", UpgradeDestroyBonusPlacementLegacyRow},
		{" Merge_Existing_Stack_Legacy_Row ", UpgradeDestroyBonusPlacementLegacyRow},
		{"nonsense", UpgradeDestroyBonusPlacementMerge},
	}
	for _, testCase := range cases {
		t.Setenv(UpgradeDestroyBonusPlacementEnv, testCase.env)
		if got := upgradeDestroyBonusPlacementMode(); got != testCase.want {
			t.Fatalf("env=%q mode=%q, want %q", testCase.env, got, testCase.want)
		}
	}
}

// ⑧ 身份冲突的行不得并（与宠物饲料/邮件/掉落同一条仓库层判据）：
// 既有行写了另一个 pvf_path ⇒ 另开一格，既有行不动。
func TestOwnerUpgradeDestroyBonusRefusesIdentityConflict(t *testing.T) {
	conflicting := destroyBonusLiveStack(3243, 9)
	conflicting.Extra = map[string]string{
		"source":              "upgrade_destroy_bonus",
		"pvf_path":            "material/vestiges_other.stk",
		"item_kind":           "stackable",
		"stackable_type":      "[material]",
		"expiration_source":   "other",
		"last_grant_source":   "other",
		"cera_package_source": "490701976",
	}
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): conflicting,
		},
	})
	if got := loaded.Slots[slotKey(listTypeMain, 129)].Count; got != 9 {
		t.Fatalf("identity-conflicting row mutated: 0:129 = %d, want 9", got)
	}
	rows := stacksForItem(loaded, 3243)
	if len(rows) != 2 || rows[0] != 121 {
		t.Fatalf("rows = %v, want a new slot 121 plus the untouched 129", rows)
	}
	if result.DestroyBonusMerged || result.DestroyBonusSlot != 121 {
		t.Fatalf("placement = %+v, want slot=121 merged=false", result)
	}
}

// ⑨ 身份**一致**的行必须并（⑧的对照面：判据是"两边都写了且不同才拒绝"，
// 不是"只要写了就拒绝"）。既有行的 pvf_path 与 PVF 定义逐字相同 ⇒ 并进 0:129。
func TestOwnerUpgradeDestroyBonusMergesWhenPVFIdentityMatches(t *testing.T) {
	matching := destroyBonusLiveStack(3243, 9)
	matching.Extra = map[string]string{
		"source":              "upgrade_destroy_bonus",
		"pvf_path":            "stackable/material/vestiges_intensive.stk",
		"item_kind":           "stackable",
		"stackable_type":      "[material]",
		"last_grant_source":   "upgrade_destroy_bonus",
		"cera_package_source": "490701976",
	}
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   destroyBonusEquipment("reinforce"),
			slotKey(listTypeMain, 129): matching,
		},
	})
	if got := stacksForItem(loaded, 3243); len(got) != 1 || got[0] != 129 {
		t.Fatalf("item 3243 rows = %v, want exactly [129] (identity matched)", got)
	}
	merged := loaded.Slots[slotKey(listTypeMain, 129)]
	if merged.Count != 16 {
		t.Fatalf("0:129 count = %d, want 16 (9+7)", merged.Count)
	}
	if merged.Extra["pvf_path"] != "stackable/material/vestiges_intensive.stk" ||
		merged.Extra["cera_package_source"] != "490701976" {
		t.Fatalf("merged row lost the identity/bookkeeping keys: %v", merged.Extra)
	}
	if result.DestroyBonusSlot != 129 || !result.DestroyBonusMerged || result.DestroyBonusCreated {
		t.Fatalf("placement = %+v, want slot=129 merged=true created=false", result)
	}
}

// ⑩ 本轮根因的正向断言：默认档新开出来的那一行**必须**满足手动拖拽合并的严格判据
// dnfrepo.StackContract（item_kind=stackable + pvf_path 以 stackable/ 开头），
// 并且第二次摧毁必须并进它。缺这两个键时 op19 会被判成 swap（见 ⑫）。
func TestOwnerUpgradeDestroyBonusNewRowSatisfiesTheManualMergeContract(t *testing.T) {
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "amplify",
		bonusItem:  3244,
		bonusCount: 3,
		identity:   destroyBonusPVFIdentity(3244, 0),
		container:  destroyBonusMaterialContainer(),
		slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): destroyBonusEquipment("amplify"),
		},
	})
	row := loaded.Slots[slotKey(listTypeMain, result.DestroyBonusSlot)]
	path, limit, ok := dnfrepo.StackContract(row)
	if !ok {
		t.Fatalf("created row does not satisfy the manual-merge contract: %+v", row)
	}
	if path != "stackable/material/crystal_pure.stk" || limit != 0 {
		t.Fatalf("contract = (%q,%d), want (stackable/material/crystal_pure.stk,0)", path, limit)
	}
	if row.Extra["stackable_type"] != "[material]" || row.Extra["item_kind"] != "stackable" {
		t.Fatalf("identity keys missing: %v", row.Extra)
	}
}

// ⑪ 段满兜底：材料段 121..176 全被占满时，新格必须退回历史顺序（60），绝不丢补偿。
func TestOwnerUpgradeDestroyBonusFallsBackWhenContainerSegmentIsFull(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 9): destroyBonusEquipment("reinforce"),
	}
	for slot := int16(121); slot <= 176; slot++ {
		slots[slotKey(listTypeMain, slot)] = dnfrepo.ItemStack{ItemID: 3037, Count: 1}
	}
	result, loaded := runDestroyBonusUpgrade(t, destroyBonusCase{
		mode:       "reinforce",
		bonusItem:  3243,
		bonusCount: 7,
		identity:   destroyBonusPVFIdentity(3243, 0),
		container:  destroyBonusMaterialContainer(),
		slots:      slots,
	})
	if result.DestroyBonusSlot != 60 || result.DestroyBonusMerged {
		t.Fatalf("placement = %+v, want the legacy fallback slot 60 merged=false", result)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 60)]; got.ItemID != 3243 || got.Count != 7 {
		t.Fatalf("fallback row = %+v, want item 3243 count 7", got)
	}
}

// ⑫ 现场 op19 的根因对照（反向断言）：2026-09-28 那一档新开出来的行**不满足**
// 堆叠契约，因此 owner.Move 只能把它当 swap —— 这就是操作者看到的
// 「浓缩的纯洁之骸还是不能正常堆叠」（日志 `moveMode=swap moveCount=9`）。
// 默认档的同一操作必须变成 `stack`（⑬）。
func TestOwnerMoveCannotStackLegacyShapedDestroyBonusRow(t *testing.T) {
	t.Setenv(UpgradeDestroyBonusPlacementEnv, UpgradeDestroyBonusPlacementLegacyRow)
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): destroyBonusEquipment("reinforce"),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	moved := false
	for attempt := 0; attempt < 64 && !moved; attempt++ {
		cmd := NewUpgradeCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, UpgradeItemRequest{
			Mode: "reinforce", TargetSlotIndex: 9, TargetItemTemplateID: 700,
			MaterialSlotIndex: -1, OptionalTicketSlotIndex: -1,
		})
		cmd.UpgradeSuccessWeight = 1
		cmd.UpgradePenaltyType = 3
		cmd.UpgradeDestroyBonusItemID = 3243
		cmd.UpgradeDestroyBonusCount = 7
		cmd.UpgradeDestroyBonusIdentity = destroyBonusPVFIdentity(3243, 0)
		cmd.UpgradeDestroyBonusContainer = destroyBonusMaterialContainer()
		result, err := owner.Upgrade(ctx, cmd)
		if err != nil {
			t.Fatalf("Upgrade error = %v", err)
		}
		if result.ResultCode != upgradeResultCodeDestroy {
			continue
		}
		moved = true
	}
	if !moved {
		t.Fatalf("never reached the destroy branch")
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	legacyRow := loaded.Slots[slotKey(listTypeMain, 60)]
	if _, _, ok := dnfrepo.StackContract(legacyRow); ok {
		t.Fatalf("legacy literal unexpectedly wrote the contract keys: %v", legacyRow.Extra)
	}
	// 同 item 的第二行（手工摆一个"材料页里那一堆"）：拖过去只能 swap。
	mercy := destroyBonusLiveStack(3243, 9)
	mercy.Extra = map[string]string{
		"source": "upgrade_destroy_bonus", "item_kind": "stackable",
		"pvf_path": "stackable/material/vestiges_intensive.stk", "stackable_type": "[material]",
	}
	record := loadTestInventory(t, ctx, repos, "77")
	record.Slots[slotKey(listTypeMain, 129)] = mercy
	if err := repos.Inventory.Save(ctx, record); err != nil {
		t.Fatalf("save error = %v", err)
	}
	moveResult, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID: "acc", SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType: listTypeMain, SourceSlotIndex: 60, MoveCount: 7,
		DestinationListType: listTypeMain, DestinationSlotIndex: 129,
	}))
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if moveResult.Mode != "swap" {
		t.Fatalf("move mode = %q, want swap (a row without the stack contract cannot merge)", moveResult.Mode)
	}
	after := loadTestInventory(t, ctx, repos, "77")
	if after.Slots[slotKey(listTypeMain, 129)].Count != 7 || after.Slots[slotKey(listTypeMain, 60)].Count != 9 {
		t.Fatalf("swap must exchange the rows: 0:60=%d 0:129=%d",
			after.Slots[slotKey(listTypeMain, 60)].Count, after.Slots[slotKey(listTypeMain, 129)].Count)
	}
}

// ⑬ 修复档的同一操作必须真的并起来（⑫ 的对照面）：默认档新开的行带全契约键，
// 手工把它拖到材料页里那一行上 ⇒ moveMode=stack，两行收敛成一行。
func TestOwnerMoveStacksDestroyBonusRowsCreatedByTheFix(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): destroyBonusEquipment("amplify"),
		},
	})
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	created := false
	for attempt := 0; attempt < 64 && !created; attempt++ {
		cmd := NewUpgradeCommand(alignedcmd.Request{AccountID: "acc", SelectedCharacterID: 77}, UpgradeItemRequest{
			Mode: "amplify", TargetSlotIndex: 9, TargetItemTemplateID: 700,
			MaterialSlotIndex: -1, OptionalTicketSlotIndex: -1,
		})
		cmd.UpgradeSuccessWeight = 1
		cmd.UpgradePenaltyType = 3
		cmd.UpgradeDestroyBonusItemID = 3244
		cmd.UpgradeDestroyBonusCount = 4
		cmd.UpgradeDestroyBonusIdentity = destroyBonusPVFIdentity(3244, 0)
		cmd.UpgradeDestroyBonusContainer = destroyBonusMaterialContainer()
		result, err := owner.Upgrade(ctx, cmd)
		if err != nil {
			t.Fatalf("Upgrade error = %v", err)
		}
		if result.ResultCode != upgradeResultCodeDestroy {
			continue
		}
		created = true
	}
	if !created {
		t.Fatalf("never reached the destroy branch")
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 121)]; got.ItemID != 3244 || got.Count != 4 {
		t.Fatalf("created row = %+v, want the compensation 3244 x4 at the material-segment start 121", got)
	}
	// 手工摆一个"材料页里那一堆"（现场 0:123 的形状：带契约键的同 item 行），
	// 再把修复档新开的 121 拖过去 —— 必须是 stack。
	pile := destroyBonusLiveStack(3244, 4)
	pile.Extra = map[string]string{
		"source": "upgrade_destroy_bonus", "item_kind": "stackable",
		"pvf_path": "stackable/material/crystal_pure.stk", "stackable_type": "[material]",
	}
	loaded.Slots[slotKey(listTypeMain, 123)] = pile
	if err := repos.Inventory.Save(ctx, loaded); err != nil {
		t.Fatalf("save error = %v", err)
	}
	moveResult, err := owner.Move(ctx, NewMoveCommand(alignedcmd.Request{
		AccountID: "acc", SelectedCharacterID: 77,
	}, MoveItemspaceRequest{
		SourceListType: listTypeMain, SourceSlotIndex: 121, MoveCount: 4,
		DestinationListType: listTypeMain, DestinationSlotIndex: 123,
	}))
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if moveResult.Mode != "stack" {
		t.Fatalf("move mode = %q, want stack (the fix writes the PVF stack contract)", moveResult.Mode)
	}
	after := loadTestInventory(t, ctx, repos, "77")
	if rows := stacksForItem(after, 3244); len(rows) != 1 || rows[0] != 123 {
		t.Fatalf("item 3244 rows = %v, want exactly [123]", rows)
	}
	if got := after.Slots[slotKey(listTypeMain, 123)].Count; got != 8 {
		t.Fatalf("0:123 count = %d, want 8", got)
	}
}
