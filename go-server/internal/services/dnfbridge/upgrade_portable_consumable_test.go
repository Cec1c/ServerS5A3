package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 便携强化/增幅消耗品（一次性增幅器 / 凯丽的强化器）真源的单测与真档 PVF 回归。
//
// 判据只有运行时 PVF 的 `[action type]`；本文件钉死三件事：
//
//  1. 逐字比对记号：`[portable upgrade]`/`[portable amplify]`（含 `[new …]`）
//     才是便携消耗品；**便携式锻造炉**（`[portable genuine damage upgrade upgrede]`）
//     是同前缀的另一族，绝不能误收；
//  2. 参数口径：第 1、2 个 = 允许的当前等级窗口，第 3 个 = 十万分比乘算加成；
//  3. 真档 Script.pvf 里现场那件 10164868「一次性增幅器」逐字节解析成
//     amplify + 0..30，且 253 个 portable 模板里的锻造族一个都不被判进来。

func TestResolvePortableUpgradeConsumableActionTypes(t *testing.T) {
	cases := []struct {
		name       string
		actionType string
		params     string
		wantMode   string
		wantMin    int
		wantMax    int
		wantBonus  int
	}{
		{
			name:       "live 一次性增幅器 10164868",
			actionType: "`[portable amplify]`",
			params:     " 0 30",
			wantMode:   "amplify",
			wantMin:    0,
			wantMax:    30,
		},
		{
			name:       "凯丽的强化器",
			actionType: "`[portable upgrade]`",
			params:     " 0 30",
			wantMode:   "reinforce",
			wantMin:    0,
			wantMax:    30,
		},
		{
			name:       "成功之兆增幅器 carries the third parameter",
			actionType: "`[new portable amplify]`",
			params:     " 0 14 30000",
			wantMode:   "amplify",
			wantMin:    0,
			wantMax:    14,
			wantBonus:  30000,
		},
		{
			name:       "凯丽的高级强化器 (Lv85~90)",
			actionType: "`[portable upgrade]`",
			params:     " 10 30",
			wantMode:   "reinforce",
			wantMin:    10,
			wantMax:    30,
		},
		{
			name:       "no parameters at all → full level window",
			actionType: "`[portable upgrade]`",
			params:     "",
			wantMode:   "reinforce",
			wantMin:    0,
			wantMax:    currentUpgradePortableMaximumLevel,
		},
		{
			name:       "便携式锻造炉 is NOT a portable upgrade consumable",
			actionType: "`[portable genuine damage upgrade upgrede]`",
			params:     "",
		},
		{
			name:       "protection ticket is not portable",
			actionType: "`[protect equipment]`",
			params:     "",
		},
		{
			name:       "ticket is not portable",
			actionType: "`[equipment amplify reinforcement ticket]`",
			params:     " 10 100",
		},
		{name: "missing action type", actionType: "", params: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := "[stackable type]\n`[town and dungeon]` 0\n"
			if test.actionType != "" {
				document += "[action type]\n" + test.actionType + test.params + "\n[/action type]\n"
			}
			parsed, err := dnfpvf.Parse("stackable/fixture.stk", document)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			mode, levelMin, levelMax, bonus, ok := resolvePortableUpgradeConsumable(parsed)
			if test.wantMode == "" {
				if ok {
					t.Fatalf("must not classify: mode=%q min=%d max=%d bonus=%d", mode, levelMin, levelMax, bonus)
				}
				return
			}
			if !ok || mode != test.wantMode || levelMin != test.wantMin || levelMax != test.wantMax || bonus != test.wantBonus {
				t.Fatalf("got mode=%q min=%d max=%d bonus=%d ok=%t, want mode=%q min=%d max=%d bonus=%d ok=true",
					mode, levelMin, levelMax, bonus, ok, test.wantMode, test.wantMin, test.wantMax, test.wantBonus)
			}
		})
	}
}

// 真档 PVF 回归：现场那件消耗品与它的同族逐字节解析，锻造族必须落在门外。
func TestRealPVFPortableUpgradeConsumablesResolve(t *testing.T) {
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
		itemID    int64
		wantMode  string
		wantPath  string
		wantMin   int
		wantMax   int
		wantBonus int
	}{
		// 现场（packet_log.txt:196377）材料槽里那一行。
		{itemID: 10164868, wantMode: "amplify", wantPath: "stackable/10164001/10164868.stk", wantMin: 0, wantMax: 30},
		{itemID: 10303750, wantMode: "amplify", wantPath: "stackable/10303001/10303750.stk", wantMin: 0, wantMax: 30},
		{itemID: 490702022, wantMode: "amplify", wantPath: "stackable/cash/chn_490702022.stk", wantMin: 0, wantMax: 50},
		{itemID: 490006651, wantMode: "reinforce", wantPath: "stackable/490006001/chn_490006651.stk", wantMin: 0, wantMax: 30},
		{itemID: 490702163, wantMode: "reinforce", wantPath: "stackable/cash/chn_490702163.stk", wantMin: 10, wantMax: 30},
		// 真档唯一带第 3 参数的形态。
		{itemID: 10007838, wantMode: "reinforce", wantPath: "stackable/cash/chn_20150507_New_Strengthen/chn_upgrade_use_10007838.stk", wantMin: 0, wantMax: 14, wantBonus: 30000},
		{itemID: 10007839, wantMode: "amplify", wantPath: "stackable/cash/chn_20150507_New_Strengthen/chn_amplify_use_10007839.stk", wantMin: 0, wantMax: 14, wantBonus: 30000},
		// 反例：便携式锻造炉（锻造路径）与保护券都不属于这一族。
		{itemID: 10164035},
		{itemID: 490007307},
		{itemID: 10303749},
	}
	for _, test := range cases {
		definition, err := catalog.ResolveItem(uint32(test.itemID))
		if err != nil {
			t.Fatalf("resolve item=%d: %v", test.itemID, err)
		}
		document, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if err != nil {
			t.Fatalf("parse item=%d path=%s: %v", test.itemID, definition.PVFPath, err)
		}
		mode, levelMin, levelMax, bonus, ok := resolvePortableUpgradeConsumable(document)
		if test.wantMode == "" {
			if ok {
				t.Fatalf("item=%d path=%s must not be classified as a portable consumable: mode=%s", test.itemID, definition.PVFPath, mode)
			}
			continue
		}
		if !ok || mode != test.wantMode || levelMin != test.wantMin || levelMax != test.wantMax || bonus != test.wantBonus {
			t.Fatalf("item=%d path=%s got mode=%q min=%d max=%d bonus=%d ok=%t, want %q %d..%d bonus=%d",
				test.itemID, definition.PVFPath, mode, levelMin, levelMax, bonus, ok,
				test.wantMode, test.wantMin, test.wantMax, test.wantBonus)
		}
		if definition.PVFPath != test.wantPath {
			t.Fatalf("item=%d path=%s, want %s", test.itemID, definition.PVFPath, test.wantPath)
		}
	}
}
