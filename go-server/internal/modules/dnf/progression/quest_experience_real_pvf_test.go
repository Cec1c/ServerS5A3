package progression

import (
	"context"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实运行时 PVF 上的 EXP 公式钉子（`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf>` 才跑）。
//
// 现场报障：「主线给的经验少 lv10-lv11导致做任务必须要刷一把白图才能拯救赛丽亚」。
// 「营救赛丽亚」= 任务 3149（`n_quest/Quest_Renewal/Epic_Quest/ElvenGard/elvengard_epic_05.qst`，
// `[level] 11 99`、`[difficulty] 1`、`[grade] [epic]`），所以这里的锚点就是
// "11 级角色做 11 级 `[difficulty] 1` 的 `[epic]` 主线"。
//
// 改动前的读数（旧公式 `penalty * ((weight*base/100)/100)`）：
//   - `[difficulty] 1`（weight 1780）：42,700（比参考实现多 6.4 倍）
//   - `[difficulty] G`（weight 30） ：700（比参考实现少 3.4 倍）
//
// 新公式（参考实现 `QuestRewardProjector.cs:175-199`）：
//
//	weighted = base * (1000 + weight) / 1000
//	value    = weighted * penalty / 100
func TestRealScriptPVFQuestExperienceUsesVendorPermilleFormula(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real quest experience formula")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}

	// ① 罚则 section 的真实形状是 `minDiff maxDiff percent`，百分数在最后。
	// 旧实现取第一个整数 ⇒ green=7 / grey=12（把 7%/12% 当成乘数）。
	if tables.quest.greenPenalty != 80 || tables.quest.greyPenalty != 30 {
		t.Fatalf("real penalties green=%d grey=%d, want 80/30", tables.quest.greenPenalty, tables.quest.greyPenalty)
	}
	if tables.quest.epicGreenPenalty != 100 || tables.quest.epicGreyPenalty != 100 {
		t.Fatalf("real epic penalties green=%d grey=%d, want 100/100", tables.quest.epicGreenPenalty, tables.quest.epicGreyPenalty)
	}
	// ② `[difficulty]` 权重（千分 additive）。
	if got := tables.quest.difficultyWeight["1"]; got != 1780 {
		t.Fatalf("real difficulty `1` weight = %d, want 1780", got)
	}
	if got := tables.quest.difficultyWeight["G"]; got != 30 {
		t.Fatalf("real difficulty `G` weight = %d, want 30", got)
	}
	// ③ exp reward table 第 11 行（任务 3149 的 `[level]` 下限）。
	if len(tables.quest.baseByLevel) < 11 || tables.quest.baseByLevel[10] != 2403 {
		t.Fatalf("real exp reward row 11 = %v, want 2403", tables.quest.baseByLevel)
	}
	// ④ 两位数档位 `10`..`17` 是独立键：378 个真实主线在 `[difficulty]` 里写的
	// 是两位数，旧实现按单 rune 建表会把这些键整档丢掉。
	if got := tables.quest.difficultyWeight["10"]; got != 708 {
		t.Fatalf("real difficulty `10` weight = %d, want 708", got)
	}
	if got := tables.quest.difficultyWeight["17"]; got != 734 {
		t.Fatalf("real difficulty `17` weight = %d, want 734", got)
	}

	tests := []struct {
		name        string
		playerLevel int
		questLevel  int
		difficulty  string
		grade       string
		want        uint32
		oldValue    uint32
	}{
		// 3149 现场读数：11 级做 11 级 `[difficulty] 1` 的 `[epic]` 主线。
		// 2403*2780/1000 = 6680（等级差 0 ⇒ 罚则 100）。
		{name: "quest 3149 at level 11", playerLevel: 11, questLevel: 11, difficulty: "1", grade: "[epic]", want: 6680, oldValue: 42700},
		// epic 罚则在本 PVF 里是 100（不罚），所以超 7 级也不掉经验。
		{name: "epic exempt from green penalty", playerLevel: 18, questLevel: 11, difficulty: "1", grade: "[epic]", want: 6680, oldValue: 42700},
		// 常见难度 `G`（weight 30）：2403*1030/1000 = 2475（旧读数只有 700）。
		{name: "common difficulty G", playerLevel: 11, questLevel: 11, difficulty: "G", grade: "[normal]", want: 2475, oldValue: 700},
		// 等级差 7 ⇒ green 80%：2475*80/100 = 1980。
		{name: "common difficulty G green penalty", playerLevel: 18, questLevel: 11, difficulty: "G", grade: "[normal]", want: 1980, oldValue: 560},
		// 等级差 12 ⇒ grey 30%：2475*30/100 = 742。
		{name: "common difficulty G grey penalty", playerLevel: 23, questLevel: 11, difficulty: "G", grade: "[normal]", want: 742, oldValue: 210},
		// 两位数档位 `10`（weight 708）：2403*1708/1000 = 4104；
		// 修复前被静默退回 `G`(30)，读数只有 2475。
		{name: "two digit difficulty 10", playerLevel: 11, questLevel: 11, difficulty: "10", grade: "[epic]", want: 4104, oldValue: 2475},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tables.QuestExperience(test.playerLevel, test.questLevel, test.difficulty, test.grade, false)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("real QuestExperience = %d, want %d (pre-fix reading %d)", got, test.want, test.oldValue)
			}
		})
	}
}
