package quest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFEveryQuestDifficultyKeyResolves 是两位数难度修复的**不回归**审计。
//
// 修复前：长度不为 1 的 `[difficulty]` 在 PlanFinishReward 里被静默换成默认 `G`，
// 永远不会走到 `ErrQuestDifficulty`。修复后改成整 token 查表，所以必须证明真档里
// 不存在"用了但权重表没有"的**多字符**键，否则会把基线能结算（虽然少给经验）的任务
// 变成无法结算。
//
// 真实 PVF 审计：`[difficulty]` 权重表 40 键。目录范围内（本测试实测
// resolved=2641 / multi-character=243 / unknown=0 / empty=450）没有未知键；再把
// `n_quest` 下全部 .qst（8920 个带难度）扫一遍，使用的规范化键同样是 40 个，未命中
// 的只有单字符 `R`(19) 与 `0`(1) —— 基线只在单 rune 时投影难度，所以这两个在基线里
// 同样落到 `ErrQuestDifficulty`，行为不变；两位数档 `10`..`17` 全部在表内。
// 注：归档 .qst 里有 378 个两位数难度定义，catalog 只覆盖 `quest.lst` 的 243 个，
// 两者都不是"当前可接任务"的全集。
func TestRealScriptPVFEveryQuestDifficultyKeyResolves(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit two-digit quest difficulty keys")
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
	var resolved, multiCharacter, unknownSingle, empty int
	var unknown []string
	for _, definition := range catalog.ordered {
		key := strings.ToUpper(strings.TrimSpace(definition.Difficulty))
		if key == "" {
			// 空值仍走 PlanFinishReward 的默认 `G`。
			empty++
			continue
		}
		if _, err := tables.QuestExperience(1, 1, key, definition.Grade, false); errors.Is(err, progression.ErrQuestDifficulty) {
			if len([]rune(key)) > 1 {
				t.Errorf("quest %d [difficulty]=%q is multi-character but absent from the weight table: baseline settled it with G, this candidate would fail closed", definition.ID, key)
			}
			unknownSingle++
			if len(unknown) < 6 {
				unknown = append(unknown, fmt.Sprintf("%d:%s", definition.ID, key))
			}
			continue
		} else if err != nil {
			t.Fatalf("quest %d difficulty %q unexpected error %v", definition.ID, key, err)
		}
		resolved++
		if len([]rune(key)) > 1 {
			multiCharacter++
		}
	}
	if multiCharacter == 0 {
		t.Fatal("no multi-character difficulty quest found in the real catalog")
	}
	t.Logf("quest difficulty audit: resolved=%d multi-character=%d unknown-single=%d empty=%d unknown_sample=%v",
		resolved, multiCharacter, unknownSingle, empty, unknown)
}

// TestPlanFinishRewardFallsBackToGForMissingDifficulty 固定空 `[difficulty]` 仍取默认 `G`。
func TestPlanFinishRewardFallsBackToGForMissingDifficulty(t *testing.T) {
	definition := Definition{
		ID: 1, Path: "n_quest/x.qst", Type: "[clear map]", LevelMin: 1, LevelMax: 99,
		Difficulty: "", RewardType: "[item]",
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{1: definition}}
	plan, err := catalog.PlanFinishReward(CharacterEligibility{Level: 1, Job: 2, GrowType: 0}, 1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Difficulty != "G" {
		t.Fatalf("empty [difficulty] plan = %q, want \"G\"", plan.Difficulty)
	}
}
