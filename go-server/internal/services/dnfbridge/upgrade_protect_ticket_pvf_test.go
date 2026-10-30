package dnfbridge

import (
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 保护券真源的单测与真档 PVF 回归。
//
// 判据只有运行时 PVF 的 `[action type]`（与厂商参考
// `ItemUpgradeConsumableResolver.FromActionType` 一致）；本文件钉死三件事：
//
//  1. 归一化：只有 `[protect equipment]` / `[amplify protect equipment]`
//     （含 `[new …]` 变体）被判成保护券，其它 `[action type]` 一律不是；
//  2. `[action type]` 段里的整数 = 失败回落等级（缺省 0）；
//  3. 真档 Script.pvf 里那 9 张券逐字节解析出正确的模式；回落等级由测试直接读取
//     同一份原始文档得到，且非券模板（一次性增幅器、材料、装备）一个都不许被判成券。

func TestUpgradeProtectTicketActionTypeNormalization(t *testing.T) {
	cases := []struct {
		action string
		want   string
	}{
		{action: "`[protect equipment]`", want: "reinforce"},
		{action: "`[amplify protect equipment]`", want: "amplify"},
		{action: "`[new protect equipment]`", want: "reinforce"},
		{action: "`[new amplify protect equipment]`", want: "amplify"},
		{action: "`[PROTECT EQUIPMENT]`", want: "reinforce"},
		{action: "`[portable amplify]`", want: ""},
		{action: "`[portable upgrade]`", want: ""},
		{action: "`[equipment reinforcement ticket]`", want: ""},
		{action: "`[limited cube]`", want: ""},
		{action: "`[add damage font skin]`", want: ""},
		{action: "`[gm quest clear]`", want: ""},
		{action: "", want: ""},
	}
	for _, test := range cases {
		if got := currentUpgradeProtectTicketModeForAction(test.action); got != test.want {
			t.Fatalf("action=%q mode=%q, want %q", test.action, got, test.want)
		}
	}
}

// realPVFActionTypeRetainLevel reads the expectation from the item's source
// document. It deliberately does not call the protect-ticket resolver under
// test, so a resolver that ignores or invents [action type] parameters cannot
// make its own integration test pass.
func realPVFActionTypeRetainLevel(
	t *testing.T,
	archive *platformpvf.Archive,
	catalog *pvfDungeonDropCatalog,
	itemID int64,
) (string, int) {
	t.Helper()
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		t.Fatalf("resolve source item=%d: %v", itemID, err)
	}
	document, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
	if err != nil {
		t.Fatalf("parse source document %s: %v", definition.PVFPath, err)
	}
	values := document.Ints("action type")
	if len(values) == 0 || values[0] <= 0 {
		return definition.PVFPath, 0
	}
	return definition.PVFPath, int(values[0])
}

func TestUpgradeProtectTicketResolutionReadsActionTypeParameters(t *testing.T) {
	cases := []struct {
		name       string
		document   string
		wantMode   string
		wantRetain int
	}{
		{
			name: "live ticket shape: no parameters at all",
			document: "[name]\n`装备增幅保护券`\n[stackable type]\n`[material]` 0\n" +
				"[action type]\n`[amplify protect equipment]`\n[/action type]\n",
			wantMode:   "amplify",
			wantRetain: 0,
		},
		{
			name: "synthetic shape: [action type] carries the failure retain level",
			document: "[stackable type]\n`[material]` 0\n" +
				"[action type]\n`[protect equipment]` 7\n[/action type]\n",
			wantMode:   "reinforce",
			wantRetain: 7,
		},
		{
			name:     "not a protection ticket",
			document: "[stackable type]\n`[material]` 0\n[action type]\n`[portable amplify]` 0 30\n[/action type]\n",
		},
		{
			name:     "no action type section",
			document: "[stackable type]\n`[material]` 0\n",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document, err := dnfpvf.Parse("stackable/fixture.stk", test.document)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			resolution := upgradeProtectTicketResolutionFromDocument(document, "stackable/fixture.stk")
			if resolution.Mode != test.wantMode || resolution.RetainLevel != test.wantRetain {
				t.Fatalf("resolution = %+v, want mode=%q retain=%d", resolution, test.wantMode, test.wantRetain)
			}
			if test.wantMode != "" {
				if resolution.UnitCost != 1 || resolution.PVFPath != "stackable/fixture.stk" {
					t.Fatalf("resolution = %+v, want unit cost 1 and the resolved path", resolution)
				}
			}
		})
	}
}

func TestUpgradeProtectTicketEventFieldsNameItemSlotAndSwitch(t *testing.T) {
	fact := &alignedcmd.UpgradeProtectTicket{
		Mode:        "amplify",
		Consumed:    true,
		Slot:        121,
		ItemID:      10303749,
		Remaining:   998,
		PVFPath:     "stackable/10303001/10303749.stk",
		RetainLevel: 0,
		OldLevel:    10,
		NewLevel:    0,
		Candidates:  41,
		Switch:      "consume_first_match",
	}
	fields := upgradeProtectTicketEventFields(fact)
	want := map[string]any{
		"mode":                    "amplify",
		"consumed":                true,
		"ticket_slot":             int16(121),
		"ticket_item_id":          int64(10303749),
		"ticket_left":             int64(998),
		"ticket_pvf_path":         "stackable/10303001/10303749.stk",
		"retain_level":            0,
		"old_level":               byte(10),
		"new_level":               byte(0),
		"scanned_slots":           41,
		"resolver_error":          "",
		"switch":                  "consume_first_match",
		"switch_env":              "DNFBRIDGE_UPGRADE_PROTECT_TICKET",
		"switch_default":          "consume_first_match",
		"switch_rollback_literal": "legacy",
	}
	if len(fields)%2 != 0 {
		t.Fatalf("fields must be key/value pairs: %v", fields)
	}
	got := make(map[string]any, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			t.Fatalf("field key %d is not a string: %v", i, fields[i])
		}
		got[key] = fields[i+1]
	}
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok || gotValue != wantValue {
			t.Fatalf("field %s = %#v, want %#v (all fields=%v)", key, gotValue, wantValue, fields)
		}
	}
	if upgradeProtectTicketEventFields(nil) != nil {
		t.Fatal("a nil fact must produce no fields (no event line)")
	}
	if name := upgradeProtectTicketEventName(fact); name != upgradeProtectTicketConsumedEventName {
		t.Fatalf("event name = %q, want %q", name, upgradeProtectTicketConsumedEventName)
	}
	if name := upgradeProtectTicketEventName(&alignedcmd.UpgradeProtectTicket{}); name != upgradeProtectTicketAbsentEventName {
		t.Fatalf("event name = %q, want %q", name, upgradeProtectTicketAbsentEventName)
	}
}

// 真档 PVF 回归：9 张线上保护券逐字节解析 + 反例。
//
// 取证来源（`pvfscriptedit.exe -dump-text`，Script.pvf
// sha256=afa00e752189a0e14326f92b3264ec59afb7fd1d2c12ae33b7a831c4adb505fe）。
// 路径和模式是固定断言；`[action type]` 的回落参数不写进公共测试，运行时从这些
// 原始文档逐项读取：
//
//	stackable/10093001/10093371.stk 装备强化保护券 mode=reinforce
//	stackable/10147001/10147773.stk 装备保护券     mode=reinforce
//	stackable/10164001/10164034.stk 装备强化保护券 mode=reinforce
//	stackable/10304001/10304354.stk 装备强化保护券 mode=reinforce
//	stackable/10093001/10093372.stk 装备增幅保护券 mode=amplify
//	stackable/10098001/10098989.stk 装备增幅保护券 mode=amplify
//	stackable/10164001/10164047.stk 装备增幅保护券 mode=amplify
//	stackable/10303001/10303749.stk 装备增幅保护券 mode=amplify
//	stackable/10304001/10304363.stk 装备增幅保护券 mode=amplify
func TestRealPVFUpgradeProtectTicketResolverClassifiesLiveTickets(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build real item catalog: %v", err)
	}
	cases := []struct {
		itemID   int64
		wantMode string
		wantPath string
	}{
		{itemID: 10093371, wantMode: "reinforce", wantPath: "stackable/10093001/10093371.stk"},
		{itemID: 10147773, wantMode: "reinforce", wantPath: "stackable/10147001/10147773.stk"},
		{itemID: 10164034, wantMode: "reinforce", wantPath: "stackable/10164001/10164034.stk"},
		{itemID: 10304354, wantMode: "reinforce", wantPath: "stackable/10304001/10304354.stk"},
		{itemID: 10093372, wantMode: "amplify", wantPath: "stackable/10093001/10093372.stk"},
		{itemID: 10098989, wantMode: "amplify", wantPath: "stackable/10098001/10098989.stk"},
		{itemID: 10164047, wantMode: "amplify", wantPath: "stackable/10164001/10164047.stk"},
		{itemID: 10303749, wantMode: "amplify", wantPath: "stackable/10303001/10303749.stk"},
		{itemID: 10304363, wantMode: "amplify", wantPath: "stackable/10304001/10304363.stk"},
		// 反例：一次性增幅器 `[portable amplify]`、摧毁补偿材料、强化/增幅券，以及
		// 一件装备——一个都不许被判成保护券。
		{itemID: 10303750},
		{itemID: 3243},
		{itemID: 3244},
		{itemID: 101040148},
	}
	for _, test := range cases {
		wantRetain := 0
		if test.wantMode != "" {
			rawPath, rawRetain := realPVFActionTypeRetainLevel(t, archive, catalog, test.itemID)
			if rawPath != test.wantPath {
				t.Fatalf("item=%d source path=%q, want %q", test.itemID, rawPath, test.wantPath)
			}
			wantRetain = rawRetain
		}
		resolution, err := resolveCurrentUpgradeProtectTicket(catalog, archive, test.itemID)
		if err != nil {
			t.Fatalf("resolve item=%d: %v", test.itemID, err)
		}
		if resolution.Mode != test.wantMode {
			t.Fatalf("item=%d mode=%q, want %q (resolution=%+v)", test.itemID, resolution.Mode, test.wantMode, resolution)
		}
		if test.wantMode == "" {
			continue
		}
		if resolution.PVFPath != test.wantPath {
			t.Fatalf("item=%d path=%q, want %q", test.itemID, resolution.PVFPath, test.wantPath)
		}
		// 这个预期来自上面的原始文档读取，而不是被测 resolver。
		if resolution.RetainLevel != wantRetain {
			t.Fatalf("item=%d retain level=%d, want %d from its source [action type]",
				test.itemID, resolution.RetainLevel, wantRetain)
		}
		if resolution.UnitCost != 1 {
			t.Fatalf("item=%d unit cost=%d, want 1", test.itemID, resolution.UnitCost)
		}
	}
}

// 真档 PVF 全量普查（默认跳过，设 DNFBRIDGE_REAL_PVF_PROTECT_SWEEP=1 才跑）：
// 把运行时 PVF 里**每一个 stackable** 的 `[action type]` / `[stackable type]` 都解析一遍，
// 列出三族模板（用来证伪"漏了某个 id"或"把别的东西误判进来"，也是报告里"以字节为准"
// 的原始输出）：
//
//	protect-ticket  `[action type]` = [protect equipment] / [amplify protect equipment]
//	portable        `[action type]` = [portable …]（一次性增幅器 / 凯丽的强化器）
//	upgrad-buff     `[stackable type]` = [item upgrad buff {usual,amplify} upgrade]（奥秘 / 秘药）
func TestRealPVFUpgradeProtectTicketSweep(t *testing.T) {
	if strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_PROTECT_SWEEP")) == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_PROTECT_SWEEP=1 together with DNFBRIDGE_REAL_PVF_SMOKE to sweep every stackable")
	}
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build real item catalog: %v", err)
	}
	scanned := 0
	for itemID, reference := range catalog.itemRefs {
		if reference.kind != dungeonDropItemStackable {
			continue
		}
		scanned++
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			continue
		}
		document, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if err != nil {
			continue
		}
		name, _ := document.Text("name")
		if resolution := upgradeProtectTicketResolutionFromDocument(document, definition.PVFPath); resolution.Mode != "" {
			t.Logf("protect-ticket item_id=%d mode=%s retain=%d path=%s name=%s",
				itemID, resolution.Mode, resolution.RetainLevel, definition.PVFPath, strings.TrimSpace(name))
		}
		if actionType, found := document.Text("action type"); found {
			token := strings.ToLower(strings.Trim(strings.TrimSpace(actionType), "`"))
			if strings.HasPrefix(token, "[portable") || strings.HasPrefix(token, "[new portable") {
				t.Logf("portable item_id=%d action=%s params=%v itemlevel=%v path=%s name=%s",
					itemID, token, document.Ints("action type"), document.Ints("check usable itemlevel"),
					definition.PVFPath, strings.TrimSpace(name))
			}
		}
		if stackableType, found := document.Text("stackable type"); found {
			token := strings.ToLower(strings.Trim(strings.TrimSpace(stackableType), "`"))
			if strings.HasPrefix(token, "[item upgrad buff") {
				text, _ := archive.ReadText(definition.PVFPath)
				t.Logf("upgrad-buff item_id=%d stackable_type=%s path=%s name=%s", itemID, token, definition.PVFPath, strings.TrimSpace(name))
				t.Logf("upgrad-buff-text item_id=%d <<<%s>>>", itemID, strings.Join(strings.Fields(string(text)), " "))
			}
		}
	}
	t.Logf("scanned %d stackables", scanned)
	if scanned == 0 {
		t.Fatal("no stackable was scanned: the catalog is empty")
	}
}
