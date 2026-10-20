package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Exercise the production entry boundary, rather than installing a profile
// directly in a runtime. Completing the quest affects the next entry only.
func TestRealStoryExperienceEntryCapturesOwnQuestBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	archive, table := realStoryExperienceArchive(t)
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	aiCharacters, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		dungeon, quest int64
		level          int
	}{{3, 3146, 3}, {6, 3147, 5}, {9, 3148, 9}} {
		t.Run(fmt.Sprintf("dgn%d", sample.dungeon), func(t *testing.T) {
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "19", AccountID: "account-1", Job: "0", Level: sample.level,
				Stats: map[string]int64{"fatigue": 156},
			}); err != nil {
				t.Fatal(err)
			}
			record := realStoryExperienceRecord(sample.quest, "active")
			record.CharacterID = "19"
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			service := &Service{
				options:                 options{accountID: "account-1", currentClientVersion: "official-24.9"},
				initialEquipmentArchive: archive, worldMapTable: table, worldMapResolver: resolver,
				dungeonMonsterTable: monsters, dungeonAICharacterTable: aiCharacters,
				dungeonChoice:      func(int) (int, error) { return 0, nil },
				dungeonSeed:        func() (uint32, error) { return 1, nil },
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{selectedCharacterID: 19}
			request := dungeoncmd.SelectDungeonRequest{DungeonID: uint32(sample.dungeon), Difficulty: 0}
			runtime, _, err := service.prepareDungeonRuntime(ctx, session, request)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.storyExperience != (currentDungeonStoryExperienceProfile{QuestID: sample.quest, RatePercent: 100}) {
				t.Fatalf("entry profile=%+v", runtime.storyExperience)
			}
			record.States[sample.quest] = dnfrepo.QuestState{Status: "completed"}
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			if got := currentDungeonStoryExperienceMultiplier(runtime); got != 2 {
				t.Fatalf("running dungeon lost frozen story multiplier=%v", got)
			}
			ordinary, _, err := service.prepareDungeonRuntime(ctx, session, request)
			if err != nil {
				t.Fatal(err)
			}
			if ordinary.storyExperience != (currentDungeonStoryExperienceProfile{}) {
				t.Fatalf("completed quest granted another run=%+v", ordinary.storyExperience)
			}
		})
	}
}
