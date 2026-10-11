package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestTrainingFatigueIdentityAndOrdinaryDebit(t *testing.T) {
	actualPath := "dungeon/PoongjinTrainingRoom/PoongjinTrainingRoom.dgn"
	for _, tc := range []struct {
		name string
		id   int64
		path string
		flag bool
		want bool
	}{
		{"actual", 5000, actualPath, false, true},
		{"normalized_windows_path", 5000, "DUNGEON\\PoongjinTrainingRoom\\PoongjinTrainingRoom.dgn", false, true},
		{"different_id", 3, actualPath, false, false},
		{"reused_id", 5000, "dungeon/Act1/MirkWood.dgn", false, false},
		{"missing_path", 5000, "", false, false},
		{"declared_no_fatigue", 3, "dungeon/other.dgn", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := worldmap.Dungeon{ID: tc.id, Path: tc.path}
			d.Metadata.NoFatigue = tc.flag
			if got := currentDungeonUsesNoFatigue(d); got != tc.want {
				t.Fatalf("no fatigue=%v want=%v", got, tc.want)
			}
		})
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	character := repo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Stats: map[string]int64{"fatigue": 17}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1"}, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 3, Path: "dungeon/Act1/MirkWood.dgn"}, Character: character}
	if err := s.debitDungeonRoomFatigue(&gameSession{selectedCharacterID: 99}, runtime, worldmap.DungeonRoomScene{}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repos.Character.Load(ctx, "99")
	if err != nil || loaded.Stats["fatigue"] != 16 {
		t.Fatalf("ordinary room debit lost: stats=%v err=%v", loaded.Stats, err)
	}
}
