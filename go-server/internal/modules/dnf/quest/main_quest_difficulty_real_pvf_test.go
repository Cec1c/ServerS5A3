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
// （字母与 `1`..`9`）和两位数档（`10`..`17`）。`n_quest` **归档**里有 378 个
// `[epic]` 主线（安徒恩 85、镜像贝希摩斯 58-59、卢克 87-88、时空裂缝 88）在
// `[difficulty]` 里写两位数；378 是归档 .qst 资源数，当前 `quest.lst` catalog 实际
// 只解析出 243 个（见 TestRealScriptPVFEveryQuestDifficultyKeyResolves）。旧实现按单
// rune 建表，并把长度不为 1 的难度值静默退回默认 `G`(30)，这些任务因此少给约 40% 经验。
//
// 这是本轮发现的**一类**缺口，只覆盖两位数难度档位：既不声称已解释 DOC-121 描述的
// 整条升级/接取流程，DOC-121 原文也没有指定任何具体任务。3149（`[difficulty] 1`，
// 单字符档）在这里只作对照锚点，确认单字符档位行为不变。
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
		experience uint32
	}{
		{3149, "1", 6680},    // ElvenGard_Epic_05：单字符对照。
		{3481, "10", 417127}, // mirrorbehimos_epic_04。
		{3482, "10", 417127},
		{3926, "17", 1273766}, // luke_f_epic_02_01。
		{3927, "17", 1273766},
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
		got, err := tables.QuestExperience(definition.LevelMin, plan.QuestLevel, plan.Difficulty, definition.Grade, plan.IgnoreLevel4Exp)
		if err != nil {
			t.Fatalf("quest %d QuestExperience error = %v", test.questID, err)
		}
		if got != test.experience {
			t.Fatalf("quest %d experience=%d, want %d", test.questID, got, test.experience)
		}
		fallback, err := tables.QuestExperience(definition.LevelMin, plan.QuestLevel, "G", definition.Grade, plan.IgnoreLevel4Exp)
		if err != nil {
			t.Fatalf("quest %d G fallback error = %v", test.questID, err)
		}
		t.Logf("quest %d difficulty=%s experience=%d old-G=%d", test.questID, plan.Difficulty, got, fallback)
		if plan.Difficulty != "1" && got <= fallback {
			t.Fatalf("quest %d exp=%d must exceed the old G fallback=%d", test.questID, got, fallback)
		}
	}
}
