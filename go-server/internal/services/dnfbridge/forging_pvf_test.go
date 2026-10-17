package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是 op438 锻造（别升级 / separate upgrade）运行时 PVF 解析的合成档回归：
// 表结构、校验、材料三种身份（3326 / 便携式锻造炉 / 锻造券 + 幸运锻造券）与
// 目标的武器/品级/稀有度/耐久。真档（线上同源 Script.pvf）的回归见
// forging_real_pvf_test.go。

// currentForgingTestSource 合成一份最小可跑通的 PVF：2 档锻造表、两件装备
// （武器 + 防具）、3326、便携式锻造炉、固定券（带白名单）与幸运锻造券。
func currentForgingTestSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "3326 `material/material_3326.stk`\n10164035 `forge/portable.stk`\n490000622 `forge/ticket_fixed.stk`\n10007408 `forge/lucky.stk`\n9001 `material/plain.stk`\n",
		"equipment/equipment.lst": "700 `weapon/test_sword.equ`\n701 `armor/test_armor.equ`\n",
		"etc/upgrade_separate.etc": "[table]\n" +
			"2 1 10000 1 3 2 5000 1.5\n" +
			"[/table]\n\n" +
			"[separate upgrade max]\n2\n\n" +
			"[item weights by grade]\n1 3326 2 82 3326 40\n[/item weights by grade]\n\n" +
			"[item weights by rarity]\n0.40 0.70 1 1.25 1.40 1.10 1.30\n[/item weights by rarity]\n",
		"stackable/material/material_3326.stk": "[name] `Breath`\n[stackable type] `[material]`\n",
		"stackable/material/plain.stk":         "[name] `Plain`\n[stackable type] `[material]`\n",
		"stackable/forge/portable.stk": "[name] `Portable Forge`\n[stackable type] `[town and dungeon]` 0\n" +
			"[action type]\n`[portable genuine damage upgrade upgrede]`\n[/action type]\n",
		"stackable/forge/ticket_fixed.stk": "[name] `+5 Ticket`\n[stackable type] `[etc]` 1\n" +
			"[equipment separate reinforcement ticket]\n5 100 `fixed` -1\n" +
			"[reinforcement usable item list]\n401000024 700\n[/reinforcement usable item list]\n",
		"stackable/forge/lucky.stk": "[name] `Lucky Forge`\n[stackable type] `[etc]` 1\n" +
			"[action type]\n`[lucky enchant deed]` 2\n[/action type]\n",
		"equipment/weapon/test_sword.equ": "[name] `Test Sword`\n[grade] 82\n[rarity] 4\n[equipment type] `[weapon]` 22\n[durability] 48\n",
		"equipment/armor/test_armor.equ":  "[name] `Test Armor`\n[grade] 82\n[rarity] 1\n[equipment type] `[armor]` 3\n[durability] 30\n",
	}
}

func TestCurrentForgingTableParsesArchiveShape(t *testing.T) {
	source := currentForgingTestSource()
	table, err := loadCurrentForgingTable(source)
	if err != nil {
		t.Fatalf("loadCurrentForgingTable error = %v", err)
	}
	if table.maxLevel != 2 || len(table.levels) != 2 {
		t.Fatalf("table = %+v, want max level 2 with 2 rows", table)
	}
	// [table] 4 个数一档：第 3 个 = 成功率（满值 10000）、第 4 个 = 材料权重。
	if table.levels[0].TargetLevel != 1 || table.levels[0].SuccessWeight != 10000 || table.levels[0].MaterialWeight != 1 {
		t.Fatalf("level 1 = %+v", table.levels[0])
	}
	if table.levels[1].TargetLevel != 2 || table.levels[1].SuccessWeight != 5000 || table.levels[1].MaterialWeight != 1.5 {
		t.Fatalf("level 2 = %+v", table.levels[1])
	}
	if rule := table.materialsByGrade[82]; rule.ItemTemplateID != 3326 || rule.BaseCount != 40 {
		t.Fatalf("grade 82 rule = %+v", rule)
	}
	if len(table.rarityWeights) != 7 {
		t.Fatalf("rarity weights = %v", table.rarityWeights)
	}
}

func TestCurrentForgingTableRejectsIncompleteDocuments(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "no table", text: "[separate upgrade max]\n8\n[item weights by grade]\n1 3326 1\n[item weights by rarity]\n1\n"},
		{name: "table not multiple of four", text: "[table]\n1 2 3\n[/table]\n[separate upgrade max]\n1\n[item weights by grade]\n1 3326 1\n[item weights by rarity]\n1\n"},
		{name: "max missing", text: "[table]\n2 1 10000 1\n[/table]\n[item weights by grade]\n1 3326 1\n[item weights by rarity]\n1\n"},
		{name: "max above level count", text: "[table]\n2 1 10000 1\n[/table]\n[separate upgrade max]\n4\n[item weights by grade]\n1 3326 1\n[item weights by rarity]\n1\n"},
		{name: "material rules missing", text: "[table]\n2 1 10000 1\n[/table]\n[separate upgrade max]\n1\n[item weights by rarity]\n1\n"},
		{name: "rarity weights missing", text: "[table]\n2 1 10000 1\n[/table]\n[separate upgrade max]\n1\n[item weights by grade]\n1 3326 1\n"},
		{name: "duplicate grade", text: "[table]\n2 1 10000 1\n[/table]\n[separate upgrade max]\n1\n[item weights by grade]\n1 3326 1 1 3326 2\n[item weights by rarity]\n1\n"},
		{name: "success weight over max", text: "[table]\n2 1 20000 1\n[/table]\n[separate upgrade max]\n1\n[item weights by grade]\n1 3326 1\n[item weights by rarity]\n1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			document, err := dnfpvf.Parse(currentForgingSeparateUpgradePVFPath, tc.text)
			if err != nil {
				t.Fatalf("parse error = %v", err)
			}
			if _, err := parseCurrentForgingTable(document); err == nil {
				t.Fatal("incomplete separate-upgrade table was accepted")
			}
		})
	}
}

func TestCurrentForgingResolverClassifiesMaterialAndTarget(t *testing.T) {
	source := currentForgingTestSource()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("catalog error = %v", err)
	}
	table, err := loadCurrentForgingTable(source)
	if err != nil {
		t.Fatalf("table error = %v", err)
	}
	resolver := currentForgingResolver(catalog, source, table)

	t.Run("ordinary weapon target", func(t *testing.T) {
		resolution, err := resolver(3326, 700)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if resolution.MaterialItemID != 3326 || resolution.PortableFurnace || resolution.ForgingTicketUsable() {
			t.Fatalf("material = %+v, want plain 3326", resolution)
		}
		if !resolution.TargetIsWeapon || resolution.TargetGrade != 82 || resolution.TargetRarity != 4 || resolution.TargetDurability != 48 {
			t.Fatalf("target = %+v, want weapon grade 82 rarity 4 durability 48", resolution)
		}
		if resolution.MaxLevel != 2 || len(resolution.Levels) != 2 || len(resolution.MaterialsByGrade) != 2 || len(resolution.ItemWeightsByRarity) != 7 {
			t.Fatalf("table projection = %+v", resolution)
		}
		if level, ok := resolution.ForgingLevelFor(2); !ok || level.SuccessWeight != 5000 {
			t.Fatalf("level 2 = %+v ok=%t", level, ok)
		}
		if _, ok := resolution.ForgingLevelFor(3); ok {
			t.Fatal("level 3 must not resolve: table holds 2 levels")
		}
	})

	t.Run("armor target is not a weapon", func(t *testing.T) {
		resolution, err := resolver(3326, 701)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if resolution.TargetIsWeapon {
			t.Fatalf("armor flagged as weapon: %+v", resolution)
		}
	})

	t.Run("portable furnace material", func(t *testing.T) {
		resolution, err := resolver(10164035, 700)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if !resolution.PortableFurnace || resolution.ForgingTicketUsable() {
			t.Fatalf("portable = %+v", resolution)
		}
		if resolution.MaterialPVFPath != "stackable/forge/portable.stk" {
			t.Fatalf("material path = %q", resolution.MaterialPVFPath)
		}
	})

	t.Run("fixed ticket with whitelist", func(t *testing.T) {
		resolution, err := resolver(490000622, 700)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if !resolution.ForgingTicketUsable() || !resolution.Ticket.IsFixed || resolution.Ticket.TargetLevel != 5 || resolution.Ticket.SuccessWeight != 10000 {
			t.Fatalf("ticket = %+v", resolution.Ticket)
		}
		if len(resolution.TicketWhitelist) != 2 || resolution.TicketWhitelist[0] != 401000024 || resolution.TicketWhitelist[1] != 700 {
			t.Fatalf("whitelist = %v", resolution.TicketWhitelist)
		}
	})

	t.Run("lucky enchant deed refine ticket", func(t *testing.T) {
		resolution, err := resolver(10007408, 700)
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		ticket := resolution.Ticket
		if !resolution.ForgingTicketUsable() || !ticket.IsAdditional || ticket.TargetLevel != 1 || ticket.SuccessWeight != 5000 {
			t.Fatalf("lucky ticket = %+v", ticket)
		}
		if ticket.FailureDecrementLevel != 0 || ticket.ApplyValue != -1 {
			t.Fatalf("lucky ticket failure/apply = %+v", ticket)
		}
	})

	t.Run("non ticket and unknown materials stay unresolved", func(t *testing.T) {
		for _, material := range []int64{9001, 700, 999999, 0} {
			resolution, err := resolver(material, 700)
			if err != nil {
				t.Fatalf("resolve(%d) error = %v", material, err)
			}
			if resolution.ForgingTicketUsable() || resolution.PortableFurnace {
				t.Fatalf("material %d misclassified: %+v", material, resolution)
			}
		}
	})
}

// TestCurrentForgingOutcomeAckWireShape 钉死桥接层回包体的两种形状：
// 成功/掷点失败 = 13 字节，校验类拒绝 = 0 + u8 error。
func TestCurrentForgingOutcomeAckWireShape(t *testing.T) {
	request := currentForgingRequest{List: 0, Slot: 9, ItemID: 101011368, MaterialSlot: 121}

	if ack := buildCurrentForgingOutcomeAck(request, nil); len(ack) != 2 || ack[0] != 0 || ack[1] != currentForgingFailureInvalidTarget {
		t.Fatalf("nil outcome ack = %x, want failure body 0 + %d", ack, currentForgingFailureInvalidTarget)
	}

	rejected := &alignedcmd.ForgingOutcome{Rejected: true, ErrorCode: 22, TargetList: 0, TargetSlot: 9, MaterialSlot: 121}
	if ack := buildCurrentForgingOutcomeAck(request, rejected); len(ack) != 2 || ack[0] != 0 || ack[1] != 22 {
		t.Fatalf("rejected ack = %x, want 00 16", ack)
	}

	applied := &alignedcmd.ForgingOutcome{
		TargetList: 0, TargetSlot: 9, MaterialSlot: 121,
		OldLevel: 2, NewLevel: 3, Succeeded: true, MaterialRemaining: 100,
	}
	ack := buildCurrentForgingOutcomeAck(request, applied)
	if len(ack) != currentForgingAckBodySize || ack[0] != 1 || ack[8] != 0 {
		t.Fatalf("applied ack = %x, want 13 byte success body", ack)
	}
	if got := int(ack[7]); got != 2 {
		t.Fatalf("old level byte = %d, want 2", got)
	}
	if got := int(ack[9]); got != 3 {
		t.Fatalf("new level byte = %d, want 3", got)
	}

	failedRoll := &alignedcmd.ForgingOutcome{
		TargetList: 3, TargetSlot: 11, MaterialSlot: 121,
		OldLevel: 4, NewLevel: 4, Succeeded: false, MaterialRemaining: 0,
	}
	// 穿戴目标（list 3）的坐标来自请求体本身：回包要回报单时用的键，
	// 领域层只在最外层拒绝了才可能不一致（那种情况走失败体）。
	wornRequest := request
	wornRequest.List = 3
	wornRequest.Slot = 11
	ack = buildCurrentForgingOutcomeAck(wornRequest, failedRoll)
	if len(ack) != currentForgingAckBodySize || ack[0] != 1 || ack[8] != 1 {
		t.Fatalf("failed roll ack = %x, want success envelope with outcome 1", ack)
	}
	if ack[10] != 3 {
		t.Fatalf("target list byte = %d, want 3", ack[10])
	}
}
