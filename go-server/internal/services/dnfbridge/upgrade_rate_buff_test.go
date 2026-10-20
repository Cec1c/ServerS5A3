package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 「强化/增幅成功率 buff 药水」真源的单测与真档 PVF 回归。
//
// 两件互相独立的事实：
//
//  1. 判定只看 `[stackable type]` 首列记号（两族逐字相等），加成取
//     `[upgrade prob increase]`（厂商标签名，十万分比），时长取
//     `[stat change duration]`（毫秒）；
//  2. **PVF 副本改动确实写进去了**：同一批 item id 在**原始**生产真档上
//     `[upgrade prob increase]` 不存在（加成 0），在改过的副本上是 10000。
//     这一条就是"PVF 写入参数"的回读校验（配合报告里的逐字节 diff）。

func TestResolveCurrentUpgradeRateBuffStackableTokens(t *testing.T) {
	cases := []struct {
		name          string
		stackableType string
		bonus         string
		duration      string
		wantMode      string
		wantBonus     int
		wantDuration  int64
	}{
		{
			name:          "强化的奥秘 (usual)",
			stackableType: "`[item upgrad buff usual upgrade]` 1000",
			bonus:         " 10000",
			duration:      " 1800000 `myself`",
			wantMode:      "reinforce",
			wantBonus:     10000,
			wantDuration:  1800000,
		},
		{
			name:          "永恒的增幅的秘密 (amplify)",
			stackableType: "`[item upgrad buff amplify upgrade]` 1000",
			bonus:         " 10000",
			duration:      " 1800000 `myself`",
			wantMode:      "amplify",
			wantBonus:     10000,
			wantDuration:  1800000,
		},
		{
			name:          "3-minute event potion",
			stackableType: "`[item upgrad buff usual upgrade]` 1000",
			bonus:         " 10000",
			duration:      " 180000 `myself`",
			wantMode:      "reinforce",
			wantBonus:     10000,
			wantDuration:  180000,
		},
		{
			name:          "no bonus section yet (the pre-edit PVF shape)",
			stackableType: "`[item upgrad buff usual upgrade]` 1000",
			duration:      " 1800000 `myself`",
			wantMode:      "reinforce",
			wantDuration:  1800000,
		},
		{
			name:          "no duration ⇒ not recorded",
			stackableType: "`[item upgrad buff usual upgrade]` 1000",
			bonus:         " 10000",
			wantMode:      "reinforce",
			wantBonus:     10000,
		},
		{
			name:          "ordinary potion is not a success-rate buff",
			stackableType: "`[waste]` 4",
			bonus:         " 10000",
			duration:      " 1800000 `myself`",
		},
		{
			name:          "protect equipment ticket is not a success-rate buff",
			stackableType: "`[material]` 0",
			bonus:         " 10000",
			duration:      " 1800000 `myself`",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := "[stackable type]\n" + test.stackableType + "\n"
			if test.bonus != "" {
				document += "[upgrade prob increase]\n" + test.bonus + "\n"
			}
			if test.duration != "" {
				document += "[stat change duration]\n" + test.duration + "\n"
			}
			parsed, err := dnfpvf.Parse("stackable/fixture.stk", document)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			mode := currentUpgradeRateBuffMode(parsed, "")
			if mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", mode, test.wantMode)
			}
			if test.wantMode == "" {
				return
			}
			if bonus, found := parsed.Int("upgrade prob increase"); found != (test.wantBonus > 0) || int(bonus) != test.wantBonus {
				t.Fatalf("bonus = %d found=%t, want %d", bonus, found, test.wantBonus)
			}
			if duration, found := parsed.Int("stat change duration"); int64(duration) != test.wantDuration && found == (test.wantDuration > 0) {
				t.Fatalf("duration = %d found=%t, want %d", duration, found, test.wantDuration)
			}
		})
	}
}

// rateBuffRealPVFCase 是 15 件药水的真档坐标（与 PVF 改动的 spec 一一对应）。
type rateBuffRealPVFCase struct {
	itemID   int64
	pvfPath  string
	wantMode string
	duration int64
}

func rateBuffRealPVFCases() []rateBuffRealPVFCase {
	return []rateBuffRealPVFCase{
		{itemID: 490001533, pvfPath: "stackable/490001001/chn_490001533.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 490004594, pvfPath: "stackable/490004001/chn_490004594.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 10004688, pvfPath: "stackable/event/chn_event_20150129_rich_dungeon/chn_amplify_10004688.stk", wantMode: "amplify", duration: 180000},
		{itemID: 490006368, pvfPath: "stackable/490006001/chn_490006368.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 10004687, pvfPath: "stackable/event/chn_event_20150129_rich_dungeon/chn_inchant_10004687.stk", wantMode: "reinforce", duration: 180000},
		{itemID: 490002394, pvfPath: "stackable/490002001/chn_490002394.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 490007876, pvfPath: "stackable/490007001/chn_490007876.stk", wantMode: "reinforce", duration: 600000},
		{itemID: 490001534, pvfPath: "stackable/490001001/chn_490001534.stk", wantMode: "amplify", duration: 1800000},
		{itemID: 10006268, pvfPath: "stackable/event/chn_event_20151008_enchant_event/chn_enchant_10006268.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 10004745, pvfPath: "stackable/event/chn_event_20150129_rich_dungeon/chn_amplify_10004745.stk", wantMode: "amplify", duration: 180000},
		{itemID: 490001497, pvfPath: "stackable/490001001/chn_490001497.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 490008465, pvfPath: "stackable/490008001/chn_490008465.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 10004744, pvfPath: "stackable/event/chn_event_20150129_rich_dungeon/chn_inchant_10004744.stk", wantMode: "reinforce", duration: 180000},
		{itemID: 490007878, pvfPath: "stackable/490007001/chn_490007878.stk", wantMode: "reinforce", duration: 1800000},
		{itemID: 490005584, pvfPath: "stackable/490005001/chn_490005584.stk", wantMode: "reinforce", duration: 1800000},
	}
}

// 真档副本（**已写入** `[upgrade prob increase] 10000`）：15 件都读出 10000。
func TestRealPVFUpgradeRateBuffCarriesTheWrittenParameter(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_BUFF_EDITED"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_BUFF_EDITED to the edited Script.pvf copy")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load edited Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}
	for _, test := range rateBuffRealPVFCases() {
		resolution, err := resolveCurrentUpgradeRateBuff(catalog, archive, test.itemID)
		if err != nil {
			t.Fatalf("item=%d: %v", test.itemID, err)
		}
		if resolution.Mode != test.wantMode || resolution.BonusWeight != 10000 || resolution.DurationMillis != test.duration {
			t.Fatalf("item=%d path=%s got mode=%q bonus=%d duration=%d, want %q 10000 %d",
				test.itemID, test.pvfPath, resolution.Mode, resolution.BonusWeight, resolution.DurationMillis,
				test.wantMode, test.duration)
		}
		if resolution.PVFPath != test.pvfPath {
			t.Fatalf("item=%d path=%s, want %s", test.itemID, resolution.PVFPath, test.pvfPath)
		}
	}
}

// 原始生产真档（**未改**）：同一批 item id 一个都没有加成段（BonusWeight = 0），
// 但模式与时长照旧 —— 这就是"改动只加了那一段"的对照面。
func TestRealPVFUpgradeRateBuffWithoutTheParameterOnTheUntouchedArchive(t *testing.T) {
	// The current smoke archive contains intentional server PVF edits. A baseline
	// comparison must receive its own original archive, not assume smoke is pristine.
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_BUFF_ORIGINAL"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_BUFF_ORIGINAL to the untouched Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("load Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}
	for _, test := range rateBuffRealPVFCases() {
		resolution, err := resolveCurrentUpgradeRateBuff(catalog, archive, test.itemID)
		if err != nil {
			t.Fatalf("item=%d: %v", test.itemID, err)
		}
		if resolution.Mode != test.wantMode || resolution.DurationMillis != test.duration {
			t.Fatalf("item=%d got mode=%q duration=%d, want %q %d",
				test.itemID, resolution.Mode, resolution.DurationMillis, test.wantMode, test.duration)
		}
		if resolution.BonusWeight != 0 {
			t.Fatalf("item=%d bonus=%d on the untouched archive, want 0 (the parameter is what the PVF edit adds)",
				test.itemID, resolution.BonusWeight)
		}
	}
}
