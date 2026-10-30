package quest

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
)

// TestOwnerApplyFinishSettlementUsesFullTwoDigitDifficultyKey 走生产
// ApplyFinishSettlement 链（不是只测 Plan/QuestExperience 各自）：真档
// `[difficulty]` 的两位数档位 `10` 必须按整 token 入账，并把经验写进持久角色，
// 同轮重试命中收据、不重复发放。
//
// 夹具：**合成结算夹具** base 200 与阈值 100/250；只有难度权重 708 取自真实
// `[difficulty]` 表（千分 additive）⇒ 200*(1000+708)/1000 = 341，累计经验 341 跨过
// 100/250 两级阈值 ⇒ 等级 3。退回到旧默认 `G`(30) 只给 206，停在等级 2 —— 该分支在
// 基线 9de2ecb 上会失败。
func TestOwnerApplyFinishSettlementUsesFullTwoDigitDifficultyKey(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), finishProgressionSource{
		progression.ExperienceTablePath: "100 250 500\n",
		progression.SkillPointTablePath: "[sp table]\n1 0\n2 30\n3 30\n4 40\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty]\n`N` 100\n`G` 30\n`10` 708\n[/difficulty]\n[exp reward table]\n200 -1\n[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		ID: 3145, Path: "n_quest/finish.qst", Type: "[clear map]", LevelMin: 1, LevelMax: 99,
		Difficulty: "10", RewardType: "[item]", HasGoldReward: true,
		RewardItems: []RewardItemRule{{ItemID: 10403, Count: 2}},
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{definition.ID: definition}}
	input := finishOwnerInput(tables, finishOwnerAllocator(nil))

	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.AtomicCommitted || first.Replayed {
		t.Fatalf("first identity=%+v", first)
	}
	if first.BaseExperienceDelta != 341 || first.ExperienceDelta != 341 {
		t.Fatalf("settlement exp base=%d total=%d, want 341 (old G reading 206)",
			first.BaseExperienceDelta, first.ExperienceDelta)
	}
	if first.PreviousLevel != 1 || first.NewLevel != 3 || first.NewExperience != 341 {
		t.Fatalf("settlement progression=%+v, want level 1->3 at 341", first)
	}
	character, ok, err := repos.Character.Load(ctx, "19")
	if err != nil || !ok {
		t.Fatalf("load character ok=%t err=%v", ok, err)
	}
	if character.Level != 3 || character.Stats["exp"] != 341 {
		t.Fatalf("persisted character level=%d exp=%d, want 3/341", character.Level, character.Stats["exp"])
	}

	// 同一 completion key 重试必须命中持久收据，不重复发放经验。
	second, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || !second.AtomicCommitted || second.ExperienceDelta != 341 ||
		second.NewLevel != 3 || second.NewExperience != 341 {
		t.Fatalf("replay=%+v, want replayed receipt with the same 341", second)
	}
	replayed, ok, err := repos.Character.Load(ctx, "19")
	if err != nil || !ok {
		t.Fatalf("reload character ok=%t err=%v", ok, err)
	}
	if replayed.Level != 3 || replayed.Stats["exp"] != 341 {
		t.Fatalf("replay duplicated exp: level=%d exp=%d, want 3/341", replayed.Level, replayed.Stats["exp"])
	}
	if _, ok, err := repos.Inventory.Load(ctx, "19"); err != nil || !ok {
		t.Fatalf("inventory after replay ok=%t err=%v", ok, err)
	}
}
