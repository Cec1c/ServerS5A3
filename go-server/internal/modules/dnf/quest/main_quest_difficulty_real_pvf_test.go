package quest

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFMainQuestTwoDigitDifficultyIsHonored 是 DOC-121
// （「主线任务经验问题，正常流程中角色等级低于主线等级，需要通关一些地图才能接到主线」）
// 的两位数难度档位补证。
//
// 真实 PVF `n_quest/questParameter.etc` 的 `[difficulty]` 同时声明单字符档
// （字母与 `1`..`9`）和两位数档（`10`..`17`）。378 个真实 `[epic]` 主线
// （安徒恩 85、镜像贝希摩斯 58-59、卢克 87-88、时空裂缝 88）在 `[difficulty]` 里
// 写的就是两位数。旧实现按单 rune 建表，并把长度不为 1 的难度值静默退回默认
// `G`(30)，这些主线因此少给约 40% 经验，与现场「主线给的经验少」一致。
//
// 同时确认单字符档位（3149「营救赛丽亚」`[difficulty] 1`）仍然正确。
func TestRealScriptPVFMainQuestTwoDigitDifficultyIsHonored(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify two-digit quest difficulty")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		questID    int64
		difficulty string
	}{
		{3149, "1"},  // ElvenGard_Epic_05「营救赛丽亚」：单字符档位，修复前已正确。
		{3481, "10"}, // mirrorbehimos_epic_04：两位数档位。
		{3482, "10"},
		{3926, "17"}, // luke_f_epic_02_01。
		{3927, "17"},
	}
	for _, test := range cases {
		definition, ok := catalog.Find(test.questID)
		if !ok {
			t.Fatalf("quest %d missing from real catalog", test.questID)
		}
		plan, err := catalog.PlanFinishReward(
			CharacterEligibility{Level: definition.LevelMin, Job: 1, GrowType: 0},
			test.questID, 0, false,
		)
		if err != nil {
			t.Fatalf("quest %d PlanFinishReward error = %v", test.questID, err)
		}
		if plan.Difficulty != test.difficulty {
			t.Fatalf("quest %d plan difficulty = %q, want %q (pvf=%q): two-digit rank fell back to the default",
				test.questID, plan.Difficulty, test.difficulty, definition.Difficulty)
		}
		if plan.Difficulty == "1" {
			continue
		}
		// 两位数档位的经验必须高于旧默认 `G`(30) 的读数。
		got, err := tables.QuestExperience(definition.LevelMin, plan.QuestLevel, plan.Difficulty, definition.Grade, plan.IgnoreLevel4Exp)
		if err != nil {
			t.Fatalf("quest %d QuestExperience error = %v", test.questID, err)
		}
		fallback, err := tables.QuestExperience(definition.LevelMin, plan.QuestLevel, "G", definition.Grade, plan.IgnoreLevel4Exp)
		if err != nil {
			t.Fatalf("quest %d G fallback error = %v", test.questID, err)
		}
		if got <= fallback {
			t.Fatalf("quest %d exp=%d must exceed the old G fallback=%d", test.questID, got, fallback)
		}
	}
}
