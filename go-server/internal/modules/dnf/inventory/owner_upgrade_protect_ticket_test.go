package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场回归：op50「失败 + 惩罚=摧毁」档必须先用掉背包里的装备强化/增幅保护券，
// 用掉了装备就**不能被摧毁**、券必须**真的少一张**（运维验收判据）。
//
// 现场缺陷（2026-09-29 运维原话）：
//
//	「装备增幅保护券 和装备强化保护券系列道具 未正常生效 强化和增幅时会显示持有
//	 保护券类道具但是不实际保护装备不被破坏」
//
// 原始证据：改动前这一档只有 `delete(items, targetKey)`（owner.go 摧毁档），
// 全树没有任何一处读 PVF 的 `[protect equipment]` / `[amplify protect equipment]`。
// 客户端也从不告诉服务端用哪张券 —— 线上 packet_log.txt 的 62 条 op50 日志里
// `ticket=-1` 一条不落。厂商参考实现（C#
// InventoryItemUpgradeService.cs:179-193/239-249/561-605）是**服务端自己扫背包**：
// 找到就消耗 1 张、resultCode 由 3 变 2、等级回落到 `[action type]` 首参（缺省 0）。
//
// 这些用例断言的是**落库后的行**（loadTestInventory）+ ACK 的 resultCode，
// 不是标志位。

// protectTicketResolver 是 dnfbridge 侧解析器的测试替身：只有 id→(mode, retain)
// 命中表里写了映射的模板才是保护券，其余一律零值（"不是券"）。
func protectTicketResolver(table map[int64]alignedcmd.UpgradeProtectTicketResolution) alignedcmd.UpgradeProtectTicketResolver {
	return func(itemID int64) (alignedcmd.UpgradeProtectTicketResolution, error) {
		if resolution, ok := table[itemID]; ok {
			return resolution, nil
		}
		return alignedcmd.UpgradeProtectTicketResolution{}, nil
	}
}

// liveProtectTicketResolutions 复刻线上 9 张保护券里最常出现的两张（真实 PVF 字节见
// dnfbridge/upgrade_protect_ticket_pvf_test.go：`[action type]` 段都没有整数参数，
// 所以 RetainLevel = 0；`[stackable type] [material]`）。
func liveProtectTicketResolutions() map[int64]alignedcmd.UpgradeProtectTicketResolution {
	return map[int64]alignedcmd.UpgradeProtectTicketResolution{
		10093371: {Mode: "reinforce", RetainLevel: 0, UnitCost: 1, PVFPath: "stackable/10093001/10093371.stk"},
		10303749: {Mode: "amplify", RetainLevel: 0, UnitCost: 1, PVFPath: "stackable/10303001/10303749.stk"},
	}
}

type protectTicketCase struct {
	mode          string
	targetLevel   byte
	ticketItemID  int64
	ticketCount   int64
	ticketSlot    int16
	extraSlots    map[string]dnfrepo.ItemStack
	resolver      alignedcmd.UpgradeProtectTicketResolver
	penaltyType   int
	bonusItemID   int
	bonusCount    int
	materialSlot  int16
	legacyEnv     bool
	keepResolverN bool
}

// runProtectTicketUpgrade 真的走 owner.Upgrade 的失败档，并把落库后的背包交回。
//
// RNG 是 math/rand 全局源（Go 1.24 起 rand.Seed 已 no-op，测试里没法种种子），
// 所以用 `UpgradeSuccessWeight = 1` 把"成功"的概率压到 1e-5，重试到失败为止；
// 连续 64 次都成功（概率 1e-320）就 t.Fatalf，绝不静默跳过。
func runProtectTicketUpgrade(t *testing.T, fixture protectTicketCase) (UpgradeResult, dnfrepo.InventoryRecord, Command) {
	t.Helper()
	if fixture.legacyEnv {
		t.Setenv(UpgradeProtectTicketEnv, UpgradeProtectTicketLegacy)
	} else {
		t.Setenv(UpgradeProtectTicketEnv, UpgradeProtectTicketConsumeFirstMatch)
	}
	ctx := context.Background()
	for attempt := 0; attempt < 64; attempt++ {
		repos := dnfrepomemory.NewMemoryGroup()
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
			CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
		})
		slots := map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): protectTicketEquipment(fixture.mode, fixture.targetLevel),
		}
		for key, stack := range fixture.extraSlots {
			slots[key] = cloneStack(stack)
		}
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots})
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatalf("NewOwner error = %v", err)
		}
		cmd := NewUpgradeCommand(alignedcmd.Request{
			AccountID:                    "acc",
			SelectedCharacterID:          77,
			UpgradeProtectTicketResolver: fixture.resolver,
		}, UpgradeItemRequest{
			Mode:                    fixture.mode,
			TargetSlotIndex:         9,
			TargetItemTemplateID:    700,
			MaterialSlotIndex:       fixture.materialSlot,
			OptionalTicketSlotIndex: -1,
		})
		if fixture.keepResolverN {
			cmd.UpgradeProtectTicketResolver = nil
		}
		cmd.UpgradeSuccessWeight = 1
		cmd.UpgradePenaltyType = fixture.penaltyType
		cmd.UpgradeDestroyBonusItemID = fixture.bonusItemID
		cmd.UpgradeDestroyBonusCount = fixture.bonusCount
		result, err := owner.Upgrade(ctx, cmd)
		if err != nil {
			t.Fatalf("Upgrade error = %v", err)
		}
		if !result.Success {
			t.Fatalf("Upgrade not applied: %+v", result)
		}
		if !result.UpgradeSucceeded {
			return result, loadTestInventory(t, ctx, repos, "77"), cmd
		}
	}
	t.Fatalf("64 次都没落到失败档（successWeight=1 时该概率 < 1e-300）")
	return UpgradeResult{}, dnfrepo.InventoryRecord{}, Command{}
}

func protectTicketEquipment(mode string, level byte) dnfrepo.ItemStack {
	extra := map[string]string{
		"item_kind":      "equipment",
		"durability":     "45",
		"max_durability": "45",
	}
	if mode == "amplify" {
		extra["amplify_type"] = "3"
		extra["amplify_value"] = "5"
	}
	raw := make([]byte, currentItemListEntrySize)
	raw[0x0A] = level
	return dnfrepo.ItemStack{ItemID: 700, Count: 1, RawEntry: raw, Extra: extra}
}

func protectTicketStack(itemID int64, count int64, pvfPath string) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: itemID,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       pvfPath,
			"stackable_type": "[material]",
		},
	}
}

// ① 报障场景（强化）：包里有一张 +13 的武器和 3 张装备强化保护券，失败即摧毁档。
// 期望：resultCode=2（不是 3）、武器还在且等级归零、券 3→2、不发补偿道具。
func TestOwnerUpgradeProtectTicketConsumesReinforceTicketInsteadOfDestroying(t *testing.T) {
	result, loaded, cmd := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  3,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
		resolver: protectTicketResolver(liveProtectTicketResolutions()),
	})
	if result.ResultCode != upgradeResultCodeDowngrade {
		t.Fatalf("resultCode = %d, want %d (protected, not destroyed)", result.ResultCode, upgradeResultCodeDowngrade)
	}
	if !result.ProtectTicketConsumed || !result.ProtectTicketScanned {
		t.Fatalf("protect ticket not reported: %+v", result)
	}
	if result.NewLevel != 0 || result.OldLevel != 13 {
		t.Fatalf("levels = %d->%d, want 13->0 (PVF [action type] has no retain parameter)", result.OldLevel, result.NewLevel)
	}
	if result.ProtectTicketSlot != 130 || result.ProtectTicketItemID != 10093371 || result.ProtectTicketRemaining != 2 {
		t.Fatalf("protect ticket fact = %+v, want slot=130 item=10093371 left=2", result)
	}
	target, ok := loaded.Slots[slotKey(listTypeMain, 9)]
	if !ok {
		t.Fatal("equipment was destroyed even though a protection ticket was held")
	}
	if got := upgradeLevelOf(target); got != 0 {
		t.Fatalf("equipment level = %d, want 0", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 2 {
		t.Fatalf("protection ticket count = %d, want 2 (exactly one consumed)", got)
	}
	if slots := stacksForItem(loaded, 3243); len(slots) != 0 {
		t.Fatalf("destroy compensation granted on a protected attempt: slots=%v", slots)
	}
	// 被消耗的券那一行必须在 op14 里（客户端背包里券数才会变）。
	entries := upgradeItemUpdateEntries(cmd, result)
	if !protectTicketEntryHasSlot(entries, 130) || !protectTicketEntryHasSlot(entries, 9) {
		t.Fatalf("op14 entries = %v, want slots 9 and 130", protectTicketEntrySlots(entries))
	}
	for _, entry := range entries {
		if entry.slot == 130 && entry.stack.Count != 2 {
			t.Fatalf("op14 ticket row count = %d, want 2", entry.stack.Count)
		}
	}
}

// ② 报障场景（增幅）：同一档位的增幅券（`[amplify protect equipment]`）。
func TestOwnerUpgradeProtectTicketConsumesAmplifyTicketInsteadOfDestroying(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "amplify",
		targetLevel:  10,
		ticketItemID: 10303749,
		ticketCount:  999,
		ticketSlot:   121,
		penaltyType:  3,
		bonusItemID:  3244,
		bonusCount:   1,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 121): protectTicketStack(10303749, 999, "stackable/10303001/10303749.stk"),
		},
		resolver: protectTicketResolver(liveProtectTicketResolutions()),
	})
	if result.ResultCode != upgradeResultCodeDowngrade || !result.ProtectTicketConsumed {
		t.Fatalf("amplify protect = %+v, want resultCode=%d consumed", result, upgradeResultCodeDowngrade)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; !ok {
		t.Fatal("amplified equipment was destroyed even though an amplify protection ticket was held")
	}
	// 现场库里角色 5 的 10303749 正是 999 张（dnf_inventory_items 0:121）：
	// 消耗一张后必须是 998，而不是"没动"。
	if got := loaded.Slots[slotKey(listTypeMain, 121)].Count; got != 998 {
		t.Fatalf("amplify protection ticket count = %d, want 998", got)
	}
}

// ③ 模式必须匹配：只有增幅券时，强化失败仍然摧毁（券不能被错用，也不能被消耗）。
func TestOwnerUpgradeProtectTicketWrongModeStillDestroys(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10303749,
		ticketCount:  5,
		ticketSlot:   121,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 121): protectTicketStack(10303749, 5, "stackable/10303001/10303749.stk"),
		},
		resolver: protectTicketResolver(liveProtectTicketResolutions()),
	})
	if result.ResultCode != upgradeResultCodeDestroy {
		t.Fatalf("resultCode = %d, want %d (an amplify ticket must not protect a reinforcement)", result.ResultCode, upgradeResultCodeDestroy)
	}
	if result.ProtectTicketConsumed {
		t.Fatalf("an amplify ticket was consumed by a reinforcement attempt: %+v", result)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; ok {
		t.Fatal("equipment survived without a matching protection ticket")
	}
	if got := loaded.Slots[slotKey(listTypeMain, 121)].Count; got != 5 {
		t.Fatalf("wrong-mode ticket count = %d, want 5 (untouched)", got)
	}
	if slots := stacksForItem(loaded, 3243); len(slots) != 1 {
		t.Fatalf("destroy compensation slots = %v, want exactly one row", slots)
	}
}

// ④ 回退档：DNFBRIDGE_UPGRADE_PROTECT_TICKET=legacy 时一次 PVF 都不问，
// 即使包里就有券也照旧摧毁（= 改动前的字节行为）。
func TestOwnerUpgradeProtectTicketLegacySwitchStillDestroys(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  3,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
		resolver:  protectTicketResolver(liveProtectTicketResolutions()),
		legacyEnv: true,
	})
	if result.ResultCode != upgradeResultCodeDestroy {
		t.Fatalf("legacy resultCode = %d, want %d", result.ResultCode, upgradeResultCodeDestroy)
	}
	if result.ProtectTicketScanned || result.ProtectTicketConsumed {
		t.Fatalf("legacy must not scan at all: %+v", result)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; ok {
		t.Fatal("legacy must still destroy the equipment")
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 3 {
		t.Fatalf("legacy ticket count = %d, want 3", got)
	}
}

// ⑤ 没有券 / 没有解析器：摧毁档一字不变（既有行为）。
func TestOwnerUpgradeProtectTicketAbsentAndNilResolverKeepDestroy(t *testing.T) {
	table := liveProtectTicketResolutions()
	cases := []struct {
		name     string
		fixture  protectTicketCase
		scanned  bool
		askedMin int
	}{
		{
			name: "no ticket in the bag",
			fixture: protectTicketCase{
				mode: "reinforce", targetLevel: 13, penaltyType: 3, bonusItemID: 3243, bonusCount: 9,
				materialSlot: -1, ticketSlot: -1,
				extraSlots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 125): protectTicketStack(3243, 4, "stackable/material/vestiges_intensive.stk"),
				},
				resolver: protectTicketResolver(table),
			},
			scanned:  true,
			askedMin: 1,
		},
		{
			name: "resolver missing",
			fixture: protectTicketCase{
				mode: "reinforce", targetLevel: 13, penaltyType: 3, bonusItemID: 3243, bonusCount: 9,
				materialSlot: -1, ticketSlot: -1, keepResolverN: true,
				extraSlots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
				},
				resolver: protectTicketResolver(table),
			},
			scanned: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, loaded, _ := runProtectTicketUpgrade(t, test.fixture)
			if result.ResultCode != upgradeResultCodeDestroy {
				t.Fatalf("resultCode = %d, want %d", result.ResultCode, upgradeResultCodeDestroy)
			}
			if result.ProtectTicketScanned != test.scanned {
				t.Fatalf("ProtectTicketScanned = %t, want %t", result.ProtectTicketScanned, test.scanned)
			}
			if result.ProtectTicketCandidates < test.askedMin {
				t.Fatalf("ProtectTicketCandidates = %d, want >= %d", result.ProtectTicketCandidates, test.askedMin)
			}
			if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; ok {
				t.Fatal("equipment must be destroyed when no matching ticket exists")
			}
		})
	}
}

// ⑥ 券只剩 1 张（含 [stack limit] 1 的 10147773 形状）：消耗后整行删除。
func TestOwnerUpgradeProtectTicketDeletesExhaustedRow(t *testing.T) {
	result, loaded, cmd := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  11,
		ticketItemID: 10147773,
		ticketCount:  1,
		ticketSlot:   122,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 122): protectTicketStack(10147773, 1, "stackable/10147001/10147773.stk"),
		},
		resolver: protectTicketResolver(map[int64]alignedcmd.UpgradeProtectTicketResolution{
			10147773: {Mode: "reinforce", RetainLevel: 0, UnitCost: 1, PVFPath: "stackable/10147001/10147773.stk"},
		}),
	})
	if !result.ProtectTicketConsumed || result.ProtectTicketRemaining != 0 {
		t.Fatalf("exhausted ticket fact = %+v", result)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 122)]; ok {
		t.Fatal("exhausted protection ticket row must be deleted")
	}
	entries := upgradeItemUpdateEntries(cmd, result)
	found := false
	for _, entry := range entries {
		if entry.slot == 122 {
			found = true
			if entry.stack.ItemID != 0 {
				t.Fatalf("deleted ticket row must be sent as an empty op14 row, got item=%d", entry.stack.ItemID)
			}
		}
	}
	if !found {
		t.Fatalf("op14 entries %v must include the deleted ticket slot 122", protectTicketEntrySlots(entries))
	}
}

// ⑦ 回落等级来自 PVF `[action type]` 首参；比当前等级高时必须夹到当前等级
// （参考实现 Clamp(FailureRetainLevel, 0, oldLevel)）。
func TestOwnerUpgradeProtectTicketRetainLevelIsClampedToOldLevel(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  2,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 2, "stackable/10093001/10093371.stk"),
		},
		resolver: protectTicketResolver(map[int64]alignedcmd.UpgradeProtectTicketResolution{
			10093371: {Mode: "reinforce", RetainLevel: 20, UnitCost: 1, PVFPath: "stackable/10093001/10093371.stk"},
		}),
	})
	if result.ResultCode != upgradeResultCodeDowngrade || result.NewLevel != 13 {
		t.Fatalf("clamped retain = %+v, want level 13 (old level) and resultCode 2", result)
	}
	if got := upgradeLevelOf(loaded.Slots[slotKey(listTypeMain, 9)]); got != 13 {
		t.Fatalf("equipment level = %d, want 13", got)
	}
}

// ⑧ 解析器报错的行按"不是券"处理：不保护、不消耗，但错误必须如实记账
// （绝不因为一次读失败就多发一张保护）。
func TestOwnerUpgradeProtectTicketResolverErrorIsRecordedAndDoesNotProtect(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  3,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
		resolver: func(itemID int64) (alignedcmd.UpgradeProtectTicketResolution, error) {
			if itemID == 10093371 {
				return alignedcmd.UpgradeProtectTicketResolution{}, errors.New("boom: pvf read failed")
			}
			return alignedcmd.UpgradeProtectTicketResolution{}, nil
		},
	})
	if result.ResultCode != upgradeResultCodeDestroy {
		t.Fatalf("resultCode = %d, want %d", result.ResultCode, upgradeResultCodeDestroy)
	}
	if result.ProtectTicketScanError == "" {
		t.Fatal("resolver failure must be recorded in ProtectTicketScanError")
	}
	if result.ProtectTicketConsumed {
		t.Fatalf("a failed resolution must never consume: %+v", result)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 3 {
		t.Fatalf("ticket count = %d, want 3", got)
	}
}

// ⑨ 同一行不能既当材料又当保护券（材料槽在候选里被排除）。
func TestOwnerUpgradeProtectTicketDoesNotDoubleConsumeTheMaterialSlot(t *testing.T) {
	table := map[int64]alignedcmd.UpgradeProtectTicketResolution{
		10093371: {Mode: "reinforce", RetainLevel: 0, UnitCost: 1, PVFPath: "stackable/10093001/10093371.stk"},
	}
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  3,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		// 材料槽就指着那张券：这一档 PVF 没有材料要求，客户端给的 slot 就是"材料"。
		materialSlot: 130,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
		resolver: protectTicketResolver(table),
	})
	if result.ResultCode != upgradeResultCodeDestroy {
		t.Fatalf("resultCode = %d, want %d (the material row must not double as the protection ticket)", result.ResultCode, upgradeResultCodeDestroy)
	}
	if result.ProtectTicketConsumed {
		t.Fatalf("the material row was used as a protection ticket: %+v", result)
	}
	// 作为材料扣 1 张 ⇒ 3 → 2；不能变成 1。
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 2 {
		t.Fatalf("material ticket count = %d, want 2 (consumed once, as material)", got)
	}
}

// ⑩ 扫描顺序：多张券时取主背包槽位最小的那一张（升序），并且跳过账号共享固定槽。
func TestOwnerUpgradeProtectTicketScanOrderAndAccountSharedExclusion(t *testing.T) {
	result, loaded, _ := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  2,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 2, "stackable/10093001/10093371.stk"),
			slotKey(listTypeMain, 125): protectTicketStack(10093371, 7, "stackable/10093001/10093371.stk"),
			// 账号共享固定槽（晶块段）里的同一件东西永远不参与保护判定。
			dnfrepo.AccountSharedInventorySlotKey(354): protectTicketStack(10093371, 9, "stackable/10093001/10093371.stk"),
		},
		resolver: protectTicketResolver(liveProtectTicketResolutions()),
	})
	if result.ProtectTicketSlot != 125 {
		t.Fatalf("chosen ticket slot = %d, want 125 (lowest main-inventory slot)", result.ProtectTicketSlot)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 125)].Count; got != 6 {
		t.Fatalf("slot 125 count = %d, want 6", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 2 {
		t.Fatalf("slot 130 count = %d, want 2 (untouched)", got)
	}
	if got := loaded.Slots[dnfrepo.AccountSharedInventorySlotKey(354)].Count; got != 9 {
		t.Fatalf("account-shared slot count = %d, want 9 (never a protection-ticket candidate)", got)
	}
}

// ⑪ 保护档不写 op50 ACK 尾部的补偿块（resultCode != 3 时 ACK 逐字节与既有的
// 降级档一致），并且上报给 dnfbridge 的载荷字段完整。
func TestOwnerUpgradeProtectTicketACKExtraFieldsAndPayload(t *testing.T) {
	result, _, cmd := runProtectTicketUpgrade(t, protectTicketCase{
		mode:         "reinforce",
		targetLevel:  13,
		ticketItemID: 10093371,
		ticketCount:  3,
		ticketSlot:   130,
		penaltyType:  3,
		bonusItemID:  3243,
		bonusCount:   9,
		materialSlot: -1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
		resolver: protectTicketResolver(liveProtectTicketResolutions()),
	})
	body := buildUpgradeItemSuccessAck(result)
	want := []byte{
		1, 0, // flag, mode=reinforce
		0xFF, 0xFF, // material slot -1
		0, 0, 0, 0, // material remaining 0
		0xFF, 0xFF, // optional ticket slot -1
		0,    // reserved
		13,   // old level
		2,    // result code: protected (downgrade code, not destroy)
		0,    // new level
		0,    // reserved
		9, 0, // target slot 9
		0xFF, 0xFF, // optional ticket slot -1
		0xFF, 0xFF, 0, 0, 0, 0, // neutral NPC material tail
	}
	if len(body) != len(want) {
		t.Fatalf("protected ACK len = %d, want %d (no destroy-bonus tail): %x", len(body), len(want), body)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Fatalf("protected ACK byte %d = 0x%02X, want 0x%02X (body=%x)", i, body[i], want[i], body)
		}
	}
	payload := upgradeProtectTicketPayload(result, upgradeProtectTicketMode())
	if payload == nil {
		t.Fatal("payload must be reported for the destroy branch")
	}
	if !payload.Consumed || payload.Mode != "reinforce" || payload.Slot != 130 ||
		payload.ItemID != 10093371 || payload.Remaining != 2 || payload.RetainLevel != 0 ||
		payload.NewLevel != 0 || payload.Switch != UpgradeProtectTicketConsumeFirstMatch {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Candidates < 1 {
		t.Fatalf("payload.Candidates = %d, want >= 1", payload.Candidates)
	}
	if upgradeProtectTicketPayload(UpgradeResult{}, UpgradeProtectTicketConsumeFirstMatch) != nil {
		t.Fatal("a non-destroy attempt must not report a protect-ticket payload")
	}
	// cmd 只是为了让 helper 走完参数构造；ACK/载荷才是本用例的断言对象。
	if cmd.Mode != "reinforce" {
		t.Fatalf("cmd mode = %q", cmd.Mode)
	}
}

// ⑫ 端到端（handler 口径）：op50 请求 → ACK 的 resultCode 必须是 2（保护），
// result 里必须挂上保护券载荷（bridge 的事件就靠它），并且 ACK 后那条 op14
// 同时刷新目标格与被消耗的券格。请求体里 `ticket=-1` 与线上完全一致
// （packet_log.txt 62 条 op50 行全部 ticket=-1）。
func TestHandlerUpgradeProtectTicketSendsDowngradeAckAndTicketRefresh(t *testing.T) {
	t.Setenv(UpgradeProtectTicketEnv, UpgradeProtectTicketConsumeFirstMatch)
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9):   protectTicketEquipment("reinforce", 13),
			slotKey(listTypeMain, 130): protectTicketStack(10093371, 3, "stackable/10093001/10093371.stk"),
		},
	})
	// op50 body: u16 mode, i16 target slot, i32 target item, i16 material slot,
	// i16 optional ticket slot, i32 name length, name.
	body := []byte{
		0x00, 0x00,
		0x09, 0x00,
		0xBC, 0x02, 0x00, 0x00,
		0xFF, 0xFF,
		0xFF, 0xFF,
		0x00, 0x00, 0x00, 0x00,
	}
	var got alignedcmd.Result
	var ack alignedcmd.UpperResponse
	for attempt := 0; attempt < 64; attempt++ {
		result, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:                       uint16(dnfenum.CmdPacketUpgradeItem),
			Body:                         body,
			AccountID:                    "acc",
			SelectedCharacterID:          77,
			Repositories:                 repos,
			UpgradeProtectTicketResolver: protectTicketResolver(liveProtectTicketResolutions()),
			UpgradePolicyResolver: func(mode string, targetItemID int64, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
				return alignedcmd.UpgradePolicyResolution{
					SuccessWeight:      1, // 把成功概率压到 1e-5，重试到失败为止
					PenaltyType:        3,
					DestroyBonusItemID: 3243,
					DestroyBonusCount:  9,
				}, nil
			},
		})
		if err != nil {
			t.Fatalf("Handle error = %v", err)
		}
		if len(result.UpperResponses) == 0 {
			t.Fatalf("no response: %+v", result)
		}
		candidate := result.UpperResponses[0]
		if len(candidate.Body) > 13 && candidate.Body[12] == upgradeResultCodeDowngrade {
			got, ack = result, candidate
			break
		}
	}
	if len(ack.Body) == 0 {
		t.Fatal("64 次都没落到失败档（successWeight=1 时该概率 < 1e-300）")
	}
	if ack.MsgID != uint16(dnfenum.CmdPacketUpgradeItem) || ack.Classification != 1 {
		t.Fatalf("ack = msg 0x%04X class %d", ack.MsgID, ack.Classification)
	}
	if len(ack.Body) != 25 {
		t.Fatalf("protected ACK len = %d, want 25 (common tail, no destroy bonus): %x", len(ack.Body), ack.Body)
	}
	if ack.Body[0] != 1 || ack.Body[11] != 13 || ack.Body[12] != upgradeResultCodeDowngrade || ack.Body[13] != 0 {
		t.Fatalf("ACK = %x, want flag=1 oldLevel=13 resultCode=%d newLevel=0", ack.Body, upgradeResultCodeDowngrade)
	}
	if got.UpgradeProtectTicket == nil || !got.UpgradeProtectTicket.Consumed {
		t.Fatalf("result must carry the consumed protect ticket: %+v", got.UpgradeProtectTicket)
	}
	if got.UpgradeDestroyBonusMerge != nil {
		t.Fatalf("a protected attempt must not report a destroy compensation: %+v", got.UpgradeDestroyBonusMerge)
	}
	if !strings.Contains(got.Reason, "protectTicket=consumed slot=130 item=10093371 left=2") {
		t.Fatalf("reason must name the consumed ticket: %q", got.Reason)
	}
	updates := 0
	for _, response := range got.UpperResponses {
		if response.Classification == 0 && response.MsgID == msgItemListUpdate {
			updates++
			// list 0 + count 2 + two rows of currentItemListEntrySize (0x77 = 119)
			if len(response.Body) != 3+2*currentItemListEntrySize {
				t.Fatalf("op14 body len = %d, want %d", len(response.Body), 3+2*currentItemListEntrySize)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("op14 responses = %d, want exactly one (target + ticket row)", updates)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; !ok {
		t.Fatal("handler destroyed the equipment while a protection ticket was held")
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 2 {
		t.Fatalf("ticket count after the handler ran = %d, want 2", got)
	}
}

func protectTicketEntrySlots(entries []commonItemListEntry) []int16 {
	slots := make([]int16, 0, len(entries))
	for _, entry := range entries {
		slots = append(slots, entry.slot)
	}
	return slots
}

func protectTicketEntryHasSlot(entries []commonItemListEntry, slot int16) bool {
	for _, entry := range entries {
		if entry.slot == slot {
			return true
		}
	}
	return false
}
