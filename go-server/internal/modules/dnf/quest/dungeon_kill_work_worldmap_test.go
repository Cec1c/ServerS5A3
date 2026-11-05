package quest

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDungeonKillWorkConservativelyKeepsWorldMapHuntMonster(t *testing.T) {
	const questID int64 = 41001
	for _, tc := range []struct {
		name     string
		area     int64
		selector int64
	}{
		{name: "area 1 ordinary", area: 1, selector: -1},
		{name: "area 1 boss", area: 1, selector: -3},
		{name: "area 39 ordinary", area: 39, selector: -1},
		{name: "area 39 boss", area: 39, selector: -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &Catalog{byID: map[int64]Definition{
				questID: {
					ID: questID, Type: "[hunt monster]",
					IntData: []int64{-4, tc.area, 2, tc.selector, 3},
				},
			}}
			work := catalog.DungeonKillWork([]int64{questID}, HuntEnemyKillInput{
				DungeonID: 12345, Difficulty: 2, EnemyCode: 67890,
			})
			if !work.HuntMonster {
				t.Fatal("WorldMap hunt-monster target was prefiltered before area and rank were known")
			}
			if work.Full || work.HuntEnemy {
				t.Fatalf("unrelated work selected: %+v", work)
			}
		})
	}

	catalog := &Catalog{byID: map[int64]Definition{
		questID: {ID: questID, Type: "[hunt monster]", IntData: []int64{310, 2, 9600, 3}},
	}}
	work := catalog.DungeonKillWork([]int64{questID}, HuntEnemyKillInput{
		DungeonID: 12345, Difficulty: 2, EnemyCode: 67890,
	})
	if work.HuntMonster || work.Full || work.HuntEnemy {
		t.Fatalf("unrelated legacy four-tuple selected: %+v", work)
	}
}

func TestPlanHuntMonsterKillStillChecksWorldMapAreaAndMonsterKind(t *testing.T) {
	const (
		area1Quest  int64 = 41011
		area39Quest int64 = 41012
	)
	catalog := &Catalog{byID: map[int64]Definition{
		area1Quest: {
			ID: area1Quest, Type: "[hunt monster]",
			IntData: []int64{-4, 1, 2, -1, 2, -4, 1, 2, -3, 3},
		},
		area39Quest: {
			ID: area39Quest, Type: "[hunt monster]",
			IntData: []int64{-4, 39, 2, -1, 2, -4, 39, 2, -3, 3},
		},
	}}
	initial := int64(packTriggerChannels(2, 3))
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			area1Quest:  {Status: "active", ProgressValue: initial},
			area39Quest: {Status: "active", ProgressValue: initial},
		},
	}
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		dungeon, monster int64
		area             int64
		kind             WorldMapHuntMonsterKind
		area1, area39    int64
		completionQuest  int64
	}{
		{
			name: "area 1 ordinary", dungeon: 12345, monster: 67890, area: 1,
			kind:  WorldMapHuntMonsterOrdinary,
			area1: int64(packTriggerChannels(1, 3)), area39: initial,
			completionQuest: area1Quest,
		},
		{
			name: "area 1 boss", dungeon: 23456, monster: 78901, area: 1,
			kind:  WorldMapHuntMonsterBoss,
			area1: int64(packTriggerChannels(2, 2)), area39: initial,
			completionQuest: area1Quest,
		},
		{
			name: "area 39 ordinary", dungeon: 34567, monster: 89012, area: 39,
			kind:  WorldMapHuntMonsterOrdinary,
			area1: initial, area39: int64(packTriggerChannels(1, 3)),
			completionQuest: area39Quest,
		},
		{
			name: "area 39 boss", dungeon: 45678, monster: 90123, area: 39,
			kind:  WorldMapHuntMonsterBoss,
			area1: initial, area39: int64(packTriggerChannels(2, 2)),
			completionQuest: area39Quest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := catalog.PlanHuntMonsterKill(record, HuntMonsterKillInput{
				DungeonID: tc.dungeon, Difficulty: 2, MonsterCode: tc.monster,
				WorldMapAreaID: tc.area, MonsterKind: tc.kind,
				CompletionKey: tc.name, CompletedAt: at,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.Record.States[area1Quest].ProgressValue; got != tc.area1 {
				t.Fatalf("area 1 progress=%d want=%d", got, tc.area1)
			}
			if got := plan.Record.States[area39Quest].ProgressValue; got != tc.area39 {
				t.Fatalf("area 39 progress=%d want=%d", got, tc.area39)
			}
			if len(plan.Completions) != 1 || plan.Completions[0].QuestID != tc.completionQuest {
				t.Fatalf("completions=%+v", plan.Completions)
			}
		})
	}
}
