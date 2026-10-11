package inventory

import (
	"context"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场回归：op50 材料槽里放**便携强化/增幅消耗品**（一次性增幅器 / 凯丽的强化器）时，
// 必须走厂商的 portable 档：消耗品替代表里的材料与金币，成功率/惩罚档走同一张正常表
// （因此失败**可以摧毁**，保护券在这一档同样生效）。
//
// 现场（2026-09-28T18:36:26Z，角色 1 = test1 / dnf:10001）：
//
//	packet_log.txt:196377（全窗口唯一一条 error=174）
//	... mode=amplify target=(23,101010856) materialSlot=107 materialPVF=(3242,11)
//	    ticket=-1 name="腰带…" ... error=174; returned current-EXE op50 error ACK
//	材料槽 0:107 = item 10164868「一次性增幅器」（[action type] `[portable amplify]` 0 30，
//	[explain]「使用后， 可以对装备进行增幅。 不消耗矛盾的结晶体和金币。」）
//
// 改动前：服务端没有 portable 档 ⇒ 消耗品被当普通材料 ⇒ 表要求 3242×11 + 金币，
// 并且在看材料之前先撞"增幅属性未鉴定"的门 ⇒ 174（目标未鉴定）或 22（目标已鉴定）。

const portableAmplifierItemID int64 = 10164868

func portableAmplifierStack(count int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: portableAmplifierItemID,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       "stackable/10164001/10164868.stk",
			"stackable_type": "[town and dungeon] 0",
		},
	}
}

// portableAmplifyConsumable 复刻真档 10164868 的 portable 身份
// （`[action type] `[portable amplify]` 0 30`，无第 3 参数）。
func portableAmplifyConsumable() *alignedcmd.UpgradePortableConsumable {
	return &alignedcmd.UpgradePortableConsumable{
		ItemID:   portableAmplifierItemID,
		Mode:     "amplify",
		LevelMin: 0,
		LevelMax: 30,
		PVFPath:  "stackable/10164001/10164868.stk",
	}
}

// upgradeTicketResolverForPortable 让 UpgradeTicket 像生产解析器一样把这一行报成
// 便携消耗品（Scene=portable），其它模板一律"不是券"。
func upgradeTicketResolverForPortable(portable *alignedcmd.UpgradePortableConsumable) alignedcmd.UpgradeTicketResolver {
	return func(materialItemID int64, targetItemID int64) (alignedcmd.UpgradeTicketResolution, error) {
		if portable != nil && materialItemID == portable.ItemID {
			return alignedcmd.UpgradeTicketResolution{
				Scene:                "portable",
				PortableMode:         portable.Mode,
				PortableLevelMin:     portable.LevelMin,
				PortableLevelMax:     portable.LevelMax,
				PortableSuccessBonus: portable.SuccessBonus,
				TicketPVFPath:        portable.PVFPath,
				TargetKind:           "equipment",
			}, nil
		}
		return alignedcmd.UpgradeTicketResolution{}, nil
	}
}

type portableUpgradeCase struct {
	mode          string
	targetLevel   byte
	amplified     bool
	consumable    *alignedcmd.UpgradePortableConsumable
	consumableCnt int64
	materialSlot  int16
	extraSlots    map[string]dnfrepo.ItemStack
	penaltyType   int
	successWeight int
	expectSuccess bool
	// expectErrorCode 非零时按“错误 ACK”（2 字节：flag=0 + 错误码）验收。
	expectErrorCode byte
	bonusItemID     int
	bonusCount      int
	gold            int64
	targetExtra     map[string]string
}

// runPortableUpgradeRequest 走完整的 handler 口径（op50 body → 解析 → 便携档），
// 把结果与落库后的背包交回。
//
// RNG 是 math/rand 全局源（测试里没法种种子），所以用 `UpgradeSuccessWeight` 控制档位
// 并重试到拿到期望档为止：1 = 失败概率 0.99999；100000 = 必定成功。
func runPortableUpgradeRequest(t *testing.T, fixture portableUpgradeCase) (alignedcmd.Result, dnfrepo.InventoryRecord) {
	t.Helper()
	t.Setenv(UpgradePortableConsumableEnv, UpgradePortableConsumableVendor)
	ctx := context.Background()
	for attempt := 0; attempt < 64; attempt++ {
		repos := dnfrepomemory.NewMemoryGroup()
		gold := fixture.gold
		if gold == 0 {
			gold = 12345
		}
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
			CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": gold},
		})
		slots := map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): portableUpgradeTarget(fixture),
		}
		slot := fixture.materialSlot
		if slot == 0 {
			slot = 107
		}
		slots[slotKey(listTypeMain, slot)] = portableAmplifierStack(fixture.consumableCnt)
		for key, stack := range fixture.extraSlots {
			slots[key] = cloneStack(stack)
		}
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots})

		// op50 body: u16 mode, i16 target slot, i32 target item, i16 material slot,
		// i16 optional ticket slot, i32 name length, name.
		body := []byte{
			0x00, 0x00,
			0x09, 0x00,
			0xBC, 0x02, 0x00, 0x00,
			byte(slot), byte(slot >> 8),
			0xFF, 0xFF,
			0x00, 0x00, 0x00, 0x00,
		}
		if fixture.mode == "amplify" {
			body[0] = 0x01
		}
		got, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:                       uint16(dnfenum.CmdPacketUpgradeItem),
			Body:                         body,
			AccountID:                    "acc",
			SelectedCharacterID:          77,
			Repositories:                 repos,
			UpgradeTicketResolver:        upgradeTicketResolverForPortable(fixture.consumable),
			UpgradeProtectTicketResolver: protectTicketResolver(liveProtectTicketResolutions()),
			UpgradePolicyResolver: func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
				return alignedcmd.UpgradePolicyResolution{
					SuccessWeight:      fixture.successWeight,
					PenaltyType:        fixture.penaltyType,
					MaterialItemID:     3242,
					MaterialCount:      11,
					DestroyBonusItemID: fixture.bonusItemID,
					DestroyBonusCount:  fixture.bonusCount,
				}, nil
			},
		})
		if err != nil {
			t.Fatalf("Handle error = %v", err)
		}
		ack := got.UpperResponses[0]
		accept := false
		if fixture.expectErrorCode != 0 {
			accept = len(ack.Body) == 2 && ack.Body[0] == 0 && ack.Body[1] == fixture.expectErrorCode
		} else {
			accept = len(ack.Body) > 12 && (ack.Body[12] == upgradeResultCodeSuccess) == fixture.expectSuccess
		}
		if accept {
			return got, loadTestInventory(t, ctx, repos, "77")
		}
	}
	t.Fatalf("64 次都没落到期望档位（expectSuccess=%t successWeight=%d）", fixture.expectSuccess, fixture.successWeight)
	return alignedcmd.Result{}, dnfrepo.InventoryRecord{}
}

func portableUpgradeTarget(fixture portableUpgradeCase) dnfrepo.ItemStack {
	extra := map[string]string{
		"item_kind":      "equipment",
		"durability":     "45",
		"max_durability": "45",
	}
	if fixture.mode == "amplify" {
		extra["amplify_type"] = "3"
		extra["amplify_value"] = "5"
	}
	for key, value := range fixture.targetExtra {
		extra[key] = value
	}
	raw := make([]byte, currentItemListEntrySize)
	raw[0x0A] = fixture.targetLevel
	return dnfrepo.ItemStack{ItemID: 700, Count: 1, RawEntry: raw, Extra: extra}
}

// ① 回归：便携档下消耗品被消耗 1 张、目标 +1、表里的材料与金币**不动**。
func TestHandlerUpgradePortableConsumableConsumesTheAmplifierOnly(t *testing.T) {
	got, loaded := runPortableUpgradeRequest(t, portableUpgradeCase{
		mode:          "amplify",
		targetLevel:   10,
		consumable:    portableAmplifyConsumable(),
		consumableCnt: 3,
		materialSlot:  107,
		successWeight: 100000, // 必定成功
		expectSuccess: true,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): protectTicketStack(3242, 11, "stackable/material/crystal_clear.stk"),
		},
	})
	if !got.Handled || len(got.UpperResponses) == 0 {
		t.Fatalf("result = %+v", got)
	}
	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketUpgradeItem) || ack.Body[0] != 1 || ack.Body[12] != upgradeResultCodeSuccess {
		t.Fatalf("ACK = %x (msg 0x%04X), want success flag=1 code=0", ack.Body, ack.MsgID)
	}
	if ack.Body[13] != 11 {
		t.Fatalf("new level byte = %d, want 11", ack.Body[13])
	}
	if got := loaded.Slots[slotKey(listTypeMain, 107)].Count; got != 2 {
		t.Fatalf("amplifier count = %d, want 2 (exactly one consumed)", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 11 {
		t.Fatalf("table material count = %d, want 11 (portable replaces it, must not be touched)", got)
	}
	if got := upgradeLevelOf(loaded.Slots[slotKey(listTypeMain, 9)]); got != 11 {
		t.Fatalf("target level = %d, want 11", got)
	}
	if !strings.Contains(got.Reason, "portable=consumed item=10164868 mode=amplify level_window=0..30") {
		t.Fatalf("reason must name the portable consumable: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "success_weight=100000") {
		t.Fatalf("reason must report the final success weight: %q", got.Reason)
	}
}

// ② 关键覆盖：便携档的失败**照样能摧毁**，因此保护券在这一档同样生效
// （运维截图底部那行「持有20个装备增幅保护券」就是这个场景）。
func TestHandlerUpgradePortableConsumableFailureConsumesProtectTicket(t *testing.T) {
	got, loaded := runPortableUpgradeRequest(t, portableUpgradeCase{
		mode:          "amplify",
		targetLevel:   10,
		consumable:    portableAmplifyConsumable(),
		consumableCnt: 5,
		materialSlot:  107,
		successWeight: 1, // 失败概率 0.99999（重试到失败为止）
		expectSuccess: false,
		penaltyType:   3, // 摧毁
		bonusItemID:   3244,
		bonusCount:    1,
		extraSlots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 121): protectTicketStack(10303749, 999, "stackable/10303001/10303749.stk"),
		},
	})
	ack := got.UpperResponses[0]
	if ack.Body[12] != upgradeResultCodeDowngrade {
		t.Fatalf("resultCode = %d, want %d (protected)", ack.Body[12], upgradeResultCodeDowngrade)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 9)]; !ok {
		t.Fatal("equipment was destroyed on the portable path even though a protection ticket was held")
	}
	if got := loaded.Slots[slotKey(listTypeMain, 121)].Count; got != 998 {
		t.Fatalf("protection ticket count = %d, want 998", got)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 107)].Count; got != 4 {
		t.Fatalf("amplifier count = %d, want 4", got)
	}
	if slots := stacksForItem(loaded, 3244); len(slots) != 0 {
		t.Fatalf("destroy compensation granted on a protected attempt: %v", slots)
	}
	if !strings.Contains(got.Reason, "protectTicket=consumed slot=121 item=10303749 left=998") {
		t.Fatalf("reason must name the consumed protect ticket: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "portable=consumed item=10164868") {
		t.Fatalf("reason must name the portable consumable: %q", got.Reason)
	}
}

// ③ 回退档（legacy）：解析器不给 portable 身份 ⇒ 逐字节回到改动前——消耗品被当普通材料，
// 表材料/金币被要求，目标未鉴定时正是现场那条 174。
func TestHandlerUpgradePortableConsumableLegacyReproducesTheLiveErrors(t *testing.T) {
	t.Setenv(UpgradePortableConsumableEnv, UpgradePortableConsumableLegacy)
	cases := []struct {
		name        string
		amplified   bool
		wantErrCode byte
	}{
		// 现场的 174：目标是"有增幅类型、数值为 0"的行（legacy 红字数据）。
		{name: "unidentified target reproduces 174", amplified: false, wantErrCode: 174},
		// 目标已鉴定 ⇒ 掉到材料校验 ⇒ 22（日志里那 4 条 error=22 同族）。
		{name: "identified target falls through to 22", amplified: true, wantErrCode: upgradeErrorInvalidMaterial},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
				CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
			})
			target := portableUpgradeTarget(portableUpgradeCase{mode: "amplify", targetLevel: 10})
			if !test.amplified {
				// 复刻现场那一行：有 amplify_type，但没有数值（未鉴定）。
				target.Extra["amplify_type"] = "3"
				target.Extra["amplify_value"] = "0"
			}
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 9):   target,
					slotKey(listTypeMain, 107): portableAmplifierStack(5),
				},
			})
			body := []byte{
				0x01, 0x00,
				0x09, 0x00,
				0xBC, 0x02, 0x00, 0x00,
				0x6B, 0x00,
				0xFF, 0xFF,
				0x00, 0x00, 0x00, 0x00,
			}
			got, err := NewHandler().Handle(ctx, alignedcmd.Request{
				Opcode:                uint16(dnfenum.CmdPacketUpgradeItem),
				Body:                  body,
				AccountID:             "acc",
				SelectedCharacterID:   77,
				Repositories:          repos,
				UpgradeTicketResolver: upgradeTicketResolverForPortable(portableAmplifyConsumable()),
				UpgradePolicyResolver: func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
					return alignedcmd.UpgradePolicyResolution{
						SuccessWeight: 50000, PenaltyType: 3, MaterialItemID: 3242, MaterialCount: 11,
					}, nil
				},
			})
			if err != nil {
				t.Fatalf("Handle error = %v", err)
			}
			ack := got.UpperResponses[0]
			if ack.Body[0] != 0 || ack.Body[1] != test.wantErrCode {
				t.Fatalf("legacy ACK = %x, want flag=0 error=%d", ack.Body, test.wantErrCode)
			}
			loaded := loadTestInventory(t, ctx, repos, "77")
			if got := loaded.Slots[slotKey(listTypeMain, 107)].Count; got != 5 {
				t.Fatalf("legacy consumed the amplifier: count=%d, want 5", got)
			}
		})
	}
}

// ④ 等级窗口（厂商 AllowsConsumableCurrentLevel）：越界 = 错误码 19，且什么都不消耗。
func TestHandlerUpgradePortableConsumableLevelWindowRejects(t *testing.T) {
	consumable := portableAmplifyConsumable()
	consumable.LevelMin = 10
	consumable.LevelMax = 30
	got, loaded := runPortableUpgradeRequest(t, portableUpgradeCase{
		mode:            "amplify",
		targetLevel:     5, // 低于窗口下界
		consumable:      consumable,
		consumableCnt:   5,
		materialSlot:    107,
		successWeight:   100000,
		expectErrorCode: 19,
	})
	ack := got.UpperResponses[0]
	if ack.Body[0] != 0 || ack.Body[1] != 19 {
		t.Fatalf("ACK = %x, want flag=0 error=19 (restriction)", ack.Body)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 107)].Count; got != 5 {
		t.Fatalf("amplifier count = %d, want 5 (untouched)", got)
	}
}

// ⑤ 模式必须相同：强化消耗品用在增幅请求上 = 错误码 23。
func TestHandlerUpgradePortableConsumableWrongModeRejects(t *testing.T) {
	consumable := portableAmplifyConsumable()
	consumable.Mode = "reinforce"
	got, loaded := runPortableUpgradeRequest(t, portableUpgradeCase{
		mode:            "amplify",
		targetLevel:     10,
		consumable:      consumable,
		consumableCnt:   5,
		materialSlot:    107,
		successWeight:   100000,
		expectErrorCode: upgradeErrorWrongMode,
	})
	ack := got.UpperResponses[0]
	if ack.Body[0] != 0 || ack.Body[1] != upgradeErrorWrongMode {
		t.Fatalf("ACK = %x, want flag=0 error=%d", ack.Body, upgradeErrorWrongMode)
	}
	if got := loaded.Slots[slotKey(listTypeMain, 107)].Count; got != 5 {
		t.Fatalf("amplifier count = %d, want 5 (untouched)", got)
	}
}

// ⑥ 金币：真档 `[explain]` 写明"不消耗矛盾的结晶体和金币" ⇒ 角色金币为 0 也必须能走。
func TestHandlerUpgradePortableConsumableSkipsGold(t *testing.T) {
	got, loaded := runPortableUpgradeRequest(t, portableUpgradeCase{
		mode:          "amplify",
		targetLevel:   10,
		consumable:    portableAmplifyConsumable(),
		consumableCnt: 1,
		materialSlot:  107,
		successWeight: 100000,
		expectSuccess: true,
		gold:          1, // 表里若真扣金币就会不够
		targetExtra:   map[string]string{"amplify_upgrade_gold_10": "100000"},
	})
	ack := got.UpperResponses[0]
	if ack.Body[0] != 1 || ack.Body[12] != upgradeResultCodeSuccess {
		t.Fatalf("ACK = %x, want success (gold must not be charged)", ack.Body)
	}
	if _, ok := loaded.Slots[slotKey(listTypeMain, 107)]; ok {
		t.Fatal("the last amplifier must be deleted")
	}
}

// ⑦ 十万分比乘算加成 + 封顶：params[2]=30000 ⇒ ×1.3；90000 × 1.3 封顶到 100000
// （= 必定成功，永远不会与失败惩罚档错位）。
func TestApplyUpgradeSuccessMultiplierMultipliesAndCaps(t *testing.T) {
	cases := []struct {
		weight int64
		bonus  int
		want   int64
	}{
		{weight: 13000, bonus: 0, want: 13000},      // 无参数 = 逐字节不变
		{weight: 13000, bonus: 30000, want: 16900},  // ×1.3（真档「成功之兆」形态）
		{weight: 10000, bonus: 10000, want: 11000},  // 乘算 +10%
		{weight: 90000, bonus: 30000, want: 100000}, // 封顶 = 必定成功
		{weight: 100000, bonus: 30000, want: 100000},
		{weight: 0, bonus: 30000, want: 0},
		{weight: 13000, bonus: -1, want: 13000}, // 负数 = 不介入
	}
	for _, test := range cases {
		if got := applyUpgradeSuccessMultiplier(test.weight, test.bonus); got != test.want {
			t.Fatalf("weight=%d bonus=%d → %d, want %d", test.weight, test.bonus, got, test.want)
		}
	}
}

// ⑧ 真档「成功之兆增幅器」形态：`[new portable amplify] 0 14 30000` ⇒ 等级窗口
// 0..14、成功率 ×1.3（日志里必须能看到最终权重）。
func TestHandlerUpgradePortableConsumableAppliesTheThirdParameter(t *testing.T) {
	consumable := portableAmplifyConsumable()
	consumable.ItemID = 10007839
	consumable.LevelMin = 0
	consumable.LevelMax = 14
	consumable.SuccessBonus = 30000
	consumable.PVFPath = "stackable/cash/chn_20150507_New_Strengthen/chn_amplify_use_10007839.stk"

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): portableUpgradeTarget(portableUpgradeCase{mode: "amplify", targetLevel: 10}),
			slotKey(listTypeMain, 107): {
				ItemID: 10007839,
				Count:  2,
				Extra: map[string]string{
					"item_kind": "stackable",
					"pvf_path":  consumable.PVFPath,
				},
			},
		},
	})
	body := []byte{
		0x01, 0x00,
		0x09, 0x00,
		0xBC, 0x02, 0x00, 0x00,
		0x6B, 0x00,
		0xFF, 0xFF,
		0x00, 0x00, 0x00, 0x00,
	}
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:                uint16(dnfenum.CmdPacketUpgradeItem),
		Body:                  body,
		AccountID:             "acc",
		SelectedCharacterID:   77,
		Repositories:          repos,
		UpgradeTicketResolver: upgradeTicketResolverForPortable(consumable),
		UpgradePolicyResolver: func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
			return alignedcmd.UpgradePolicyResolution{SuccessWeight: 13000, PenaltyType: 3, MaterialItemID: 3242, MaterialCount: 11}, nil
		},
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	// 13000 × 1.3 = 16900（真档 10007839 的 `[action type]` 第 3 参数 30000）。
	if !strings.Contains(got.Reason, "success_weight=16900") || !strings.Contains(got.Reason, "success_bonus=30000") {
		t.Fatalf("reason must report the multiplied weight and the bonus: %q", got.Reason)
	}
}
