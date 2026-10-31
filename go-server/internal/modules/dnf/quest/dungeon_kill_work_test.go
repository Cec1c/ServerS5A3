package quest

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDungeonKillWorkNegativeMatchesOriginalPlans(t *testing.T) {
	// 对照原规划：任何新筛选为阴性的目标都必须没有进度/幂等通知/关联副作用。
	definitions := []Definition{
		{ID: 1, Type: "[hunt enemy]", IntData: []int64{310, 1, 99, 1, 3}},
		{ID: 2, Type: "[hunt enemy]", IntData: []int64{-1, -1, 99, 0, 1, 52, 2, 88, 3, 1}},
		{ID: 3, Type: "[hunt monster]", IntData: []int64{310, 1, 99, 3}},
		{ID: 4, Type: "[hunt monster]", IntData: []int64{-1, -1, 88, 1}},
		{ID: 5, Type: "[seeking]", IntData: []int64{123, 5}},
		{ID: 6, Type: "[raid phase clear]", IntData: []int64{10}},
	}
	for _, definition := range definitions {
		catalog := &Catalog{byID: map[int64]Definition{definition.ID: definition}}
		for _, dungeon := range []int64{52, 310, 99} {
			for _, difficulty := range []int64{0, 1, 2} {
				for _, enemy := range []int64{88, 99, 100} {
					for _, enemyType := range []int64{1, 2, 3} {
						input := HuntEnemyKillInput{DungeonID: dungeon, Difficulty: difficulty, EnemyCode: enemy,
							EnemyType: enemyType, CompletionKey: "test", CompletedAt: time.Unix(100, 0)}
						work := catalog.DungeonKillWork([]int64{definition.ID}, input)
						for _, trigger := range []int64{0, 1, 3, -1} {
							record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
								definition.ID: {Status: "active", ProgressValue: trigger},
							}}
							enemyPlan, err := catalog.PlanHuntEnemyKill(record, input)
							if err != nil || (!work.HuntEnemy && (len(enemyPlan.ChangedFields) > 0 || len(enemyPlan.Completions) > 0)) {
								t.Fatalf("杀敌筛选漏掉原规划：id=%d input=%+v trigger=%d err=%v", definition.ID, input, trigger, err)
							}
							monsterPlan, err := catalog.PlanHuntMonsterKill(record, HuntMonsterKillInput{
								DungeonID: dungeon, Difficulty: difficulty, MonsterCode: enemy, CompletionKey: "test", CompletedAt: input.CompletedAt,
							})
							if err != nil || (!work.HuntMonster && (len(monsterPlan.ChangedFields) > 0 || len(monsterPlan.Completions) > 0)) {
								t.Fatalf("杀怪筛选漏掉原规划：id=%d input=%+v trigger=%d err=%v", definition.ID, input, trigger, err)
							}
						}
					}
				}
			}
		}
	}
}

func TestDungeonKillWorkKeepsLinkedActivationAndUnknownDefinitions(t *testing.T) {
	for _, tag := range []string{"[quest clear]", "[clear quest]"} {
		catalog := &Catalog{byID: map[int64]Definition{1: {ID: 1, Type: tag, IntData: []int64{2}}}}
		work := catalog.DungeonKillWork([]int64{1}, HuntEnemyKillInput{})
		if !work.Full || !work.HuntEnemy || !work.HuntMonster {
			t.Fatal("父子任务补建及进度同步必须保留")
		}
	}
	for _, catalog := range []*Catalog{nil, {byID: map[int64]Definition{}}} {
		if !catalog.DungeonKillWork([]int64{123}, HuntEnemyKillInput{}).Full {
			t.Fatal("缺定义必须保留原处理")
		}
	}
}
