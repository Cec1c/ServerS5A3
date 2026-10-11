package quest

import (
	"context"
	"os"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestHuntEnemyAndMonsterMinimumDifficulty(t *testing.T) {
	values := []int64{311, 1, 63767, 1, 1}
	for _, difficulty := range []int64{0, 1, 2, 3, 4} {
		want := difficulty >= 1
		if _, ok := huntEnemyMatchedChannel(values, HuntEnemyKillInput{DungeonID: 311, Difficulty: difficulty, EnemyCode: 63767, EnemyType: 1}); ok != want {
			t.Fatalf("enemy difficulty %d: %t", difficulty, ok)
		}
		if _, ok := huntMonsterMatchedChannel([]int64{311, 1, 63767, 1}, HuntMonsterKillInput{DungeonID: 311, Difficulty: difficulty, MonsterCode: 63767}); ok != want {
			t.Fatalf("monster difficulty %d: %t", difficulty, ok)
		}
		catalog := &Catalog{byID: map[int64]Definition{2900: {ID: 2900, Type: "[hunt enemy]", IntData: values}}}
		record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{2900: {Status: "active", ProgressValue: 1}}}
		if (len(catalog.ActiveHuntEnemyTargets(record, 311, difficulty, 1)) > 0) != want {
			t.Fatal("entry target projection disagrees with kill matching")
		}
	}
}

func TestRealPVFAllAbyssHuntPrerequisitesCompleteAtHigherDifficulty(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	i, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{6880, 4391, 2002, 2900} {
		def, ok := c.Find(id)
		if !ok || normalizeQuestTag(def.Type) != "hunt enemy" {
			t.Fatalf("quest %d wrong type", id)
		}
		for _, difficulty := range []int64{1, 2, 3, 4} {
			record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{id: {Status: "active", ProgressValue: int64(definitionInitialTrigger(def, nil))}}}
			for offset := 0; offset+5 <= len(def.IntData); offset += 5 {
				v := def.IntData[offset : offset+5]
				plan, err := c.PlanHuntEnemyKill(record, HuntEnemyKillInput{DungeonID: v[0], Difficulty: difficulty, EnemyCode: v[2], EnemyType: v[3], CompletionKey: "authoritative-monster-death", CompletedAt: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
				record = plan.Record
			}
			if record.States[id].ProgressValue != 0 || record.States[id].Status != "active" {
				t.Fatalf("quest %d difficulty %d did not become rewardable: %+v", id, difficulty, record.States[id])
			}
			if _, err := c.PlanFinishReward(CharacterEligibility{Level: 90}, id, 0, false); err != nil {
				t.Fatalf("quest %d reward unsupported: %v", id, err)
			}
		}
	}
}
