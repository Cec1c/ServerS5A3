package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPlayerIssuesTrainingFatigueRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	listText, err := archive.ReadText("dungeon/dungeon.lst")
	if err != nil {
		t.Fatal(err)
	}
	list, err := dnfpvf.Parse("dungeon/dungeon.lst", listText)
	if err != nil {
		t.Fatal(err)
	}
	var listedPath string
	for _, entry := range dnfpvf.ParseList(list) {
		if entry.ID == 5000 {
			listedPath = entry.Path
			break
		}
	}
	if listedPath == "" {
		t.Fatal("training dungeon missing from actual list")
	}
	text, err := archive.ReadText("dungeon/" + listedPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("dungeon/"+listedPath, text)
	if err != nil {
		t.Fatal(err)
	}
	d := worldmap.ParseDungeon(5000, "dungeon/"+listedPath, doc)
	if d.Metadata.Name != "修炼道场" || d.Metadata.NoFatigue {
		t.Fatalf("unexpected real training identity/flag: %+v", d.Metadata)
	}
	t.Logf("actual dungeon.lst: 5000 %q; name=%q no-fatigue=%v", listedPath, d.Metadata.Name, d.Metadata.NoFatigue)
	t.Run("P31_zero_fatigue_admission", func(t *testing.T) {
		if err := validateDungeonEntry(repo.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 0}}, d, nil); err != nil {
			t.Fatalf("training admission at zero fatigue: %v", err)
		}
	})
	t.Run("P34_no_room_debit_or_cold_balance_loss", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		character := repo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Level: 90, Stats: map[string]int64{"fatigue": 17}}
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		s := &Service{options: options{accountID: "account-1"}, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
		session := &gameSession{selectedCharacterID: 99}
		runtime := &runtimeDungeonState{Dungeon: d, Character: character}
		scene := worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0}}
		for i := 0; i < 2; i++ {
			if err := s.debitDungeonRoomFatigue(session, runtime, scene); err != nil {
				t.Fatal(err)
			}
		}
		loaded, found, err := repos.Character.Load(ctx, "99")
		if err != nil || !found || loaded.Stats["fatigue"] != 17 {
			t.Fatalf("training charged durable fatigue: balance=%v found=%v err=%v", loaded.Stats, found, err)
		}
		cold, err := s.updateCurrentCharacterFatigue(ctx, &gameSession{selectedCharacterID: 99}, false)
		if err != nil || cold.Stats["fatigue"] != 17 {
			t.Fatalf("cold refresh balance=%v err=%v", cold.Stats, err)
		}
	})
	t.Run("ordinary_dungeon_still_requires_fatigue", func(t *testing.T) {
		ordinary := d
		ordinary.ID = 3
		ordinary.Path = "dungeon/Act1/MirkWood.dgn"
		if err := validateDungeonEntry(repo.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 0}}, ordinary, nil); !errors.Is(err, dnfdungeon.ErrEntryFatigueExhausted) {
			t.Fatalf("ordinary zero-fatigue gate lost: %v", err)
		}
	})
}
