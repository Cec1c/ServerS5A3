package quest

import (
	"context"
	"errors"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 「深渊派对任务无法完成」的回归钉子。
//
// 现场：`n_quest/hell/achievement_*_hell_*.qst`（「深渊派对Ep1..Ep5」，PVF 里一共 10 个）
// 用 `[reward type] `[hell challenge]“ + `[reward int data] 1`。这一档此前既不是
// `[item]`，也不属于 grow/awakening/expert job/slot expansion，于是
// `PlanFinishReward` 直接返回 `ErrQuestRewardUnsupported`；dnfbridge 把它算成业务失败
// （`services/dnfbridge/quest_finish.go` 的 `currentFinishQuestBusinessFailure`）
// 并回 class1/op34 失败 22 —— 任务永远无法完成。
//
// 现在这一档按"无资产奖励"受理：任务能归档（EXP/金币仍走 questParameter.etc 的
// 等级×难度规则），但**不发放任何道具/货币/标志位**：本版没有深渊派对运行时，
// 这个"深渊挑战"计数存在哪里没有任何证据。
func TestPlanFinishRewardAcceptsHellChallengeAsNoAssetReward(t *testing.T) {
	source := catalogTestSource{
		DefaultList: "4471 `hell_challenge.qst`\n4472 `unknown_reward.qst`\n4473 `malformed_hell.qst`\n",
		// 与真实 PVF 同形：`[type] `[hunt enemy]`` + `[reward type] `[hell challenge]`` + `[reward int data] 1`。
		"n_quest/hell_challenge.qst": questCatalogTestDefinition("[achievement]", 47, 99, "[all]",
			"[type]\n`[hunt enemy]`\n[int data]\n44 2 61704 1 1\n"+
				"[reward type]\n`[hell challenge]`\n[reward int data]\n1\n[/reward int data]\n"+
				"[reward selection int data]\n[/reward selection int data]\n"),
		"n_quest/unknown_reward.qst": questCatalogTestDefinition("[achievement]", 47, 99, "[all]",
			"[type]\n`[hunt enemy]`\n[int data]\n44 2 61704 1 1\n"+
				"[reward type]\n`[unproved future reward]`\n[reward int data]\n1\n[/reward int data]\n"),
		"n_quest/malformed_hell.qst": questCatalogTestDefinition("[achievement]", 47, 99, "[all]",
			"[type]\n`[hunt enemy]`\n[int data]\n44 2 61704 1 1\n"+
				"[reward type]\n`[hell challenge]`\n[reward int data]\n-3\n[/reward int data]\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	character := CharacterEligibility{Level: 47, Job: 2, GrowType: 0}

	plan, err := catalog.PlanFinishReward(character, 4471, 0, false)
	if err != nil {
		t.Fatalf("hell-challenge finish plan error = %v (was ErrQuestRewardUnsupported before the fix)", err)
	}
	if !plan.HasHellChallenge || plan.HellChallengeValue != 1 {
		t.Fatalf("hell-challenge plan = %+v, want HasHellChallenge with value 1", plan)
	}
	if len(plan.Items) != 0 || plan.HasGoldReward || plan.HasProfession || plan.HasExpertJob || plan.HasSlotExpansion {
		t.Fatalf("hell-challenge plan invented an asset: %+v", plan)
	}
	if plan.QuestID != 4471 || plan.QuestLevel != 47 {
		t.Fatalf("hell-challenge plan lost the quest coordinates: %+v", plan)
	}

	// 不能顺手把闸门全部打开：真正未知的奖励类型仍然失败关闭。
	if _, err := catalog.PlanFinishReward(character, 4472, 0, false); !errors.Is(err, ErrQuestRewardUnsupported) {
		t.Fatalf("unknown reward type error = %v, want ErrQuestRewardUnsupported", err)
	}
	// 形状不合法的 `[hell challenge]` 也必须失败关闭，而不是被当成 0。
	if _, err := catalog.PlanFinishReward(character, 4473, 0, false); !errors.Is(err, ErrQuestRewardMalformed) {
		t.Fatalf("negative hell-challenge value error = %v, want ErrQuestRewardMalformed", err)
	}
}
