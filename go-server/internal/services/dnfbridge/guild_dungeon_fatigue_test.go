package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func guildDungeonFatigueTestDungeon() worldmap.Dungeon {
	return worldmap.Dungeon{
		ID: 8500,
		Metadata: worldmap.DungeonMetadata{
			IntegerValues: map[string]int64{worldmap.DungeonUseFatigueOnlyStartKey: 8},
		},
	}
}

func TestGuildDungeonStartFatigueIsChargedOncePerRun(t *testing.T) {
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	const accountID = "fatigue-account"
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{99, 100} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: strconv.Itoa(id),
			AccountID:   accountID,
			Stats:       map[string]int64{"fatigue": 20},
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	dungeon := guildDungeonFatigueTestDungeon()
	for _, id := range []uint16{99, 100} {
		session := &gameSession{accountID: accountID, selectedCharacterID: id}
		runtime := &runtimeDungeonState{Dungeon: dungeon}
		for _, coordinate := range []worldmap.RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 0}, {X: 2, Y: 0}} {
			if err := s.debitDungeonRoomFatigue(session, runtime, worldmap.DungeonRoomScene{Coordinate: coordinate}); err != nil {
				t.Fatal(err)
			}
		}
		if !runtime.fatigueStartDebited || runtime.Character.Stats["fatigue"] != 12 {
			t.Fatalf("first run member %d state=%+v", id, runtime.Character.Stats)
		}
		stored, _, err := repos.Character.Load(ctx, strconv.Itoa(int(id)))
		if err != nil || stored.Stats["fatigue"] != 12 {
			t.Fatalf("persistent member %d fatigue=%d err=%v", id, stored.Stats["fatigue"], err)
		}
		next := &runtimeDungeonState{Dungeon: dungeon}
		if err := s.debitDungeonRoomFatigue(session, next, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0}}); err != nil {
			t.Fatal(err)
		}
		if next.Character.Stats["fatigue"] != 4 {
			t.Fatalf("new run member %d fatigue=%d", id, next.Character.Stats["fatigue"])
		}
	}
}

func TestGuildDungeonStartFatigueUsesRefreshedBalance(t *testing.T) {
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	const accountID = "refresh-fatigue-account"
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	previousDay := premium.ServiceDayID(s.gameplayNow().AddDate(0, 0, -1))
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "101",
		AccountID:   accountID,
		Stats: map[string]int64{
			"fatigue":         0,
			"fatigue_limit":   156,
			dnffatigue.DayKey: previousDay,
		},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeDungeonState{Dungeon: guildDungeonFatigueTestDungeon()}
	if err := s.debitDungeonRoomFatigue(
		&gameSession{accountID: accountID, selectedCharacterID: 101},
		runtime,
		worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0}},
	); err != nil {
		t.Fatal(err)
	}
	if runtime.Character.Stats["fatigue"] != 148 {
		t.Fatalf("refreshed fatigue=%d", runtime.Character.Stats["fatigue"])
	}
}

func TestGuildDungeonFatigueAdmissionAndOrdinaryRoomRegression(t *testing.T) {
	dungeon := guildDungeonFatigueTestDungeon()
	if err := validateDungeonEntry(dnfrepo.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 7}}, dungeon, nil); !errors.Is(err, dnfdungeon.ErrEntryFatigueExhausted) {
		t.Fatalf("seven fatigue admission err=%v", err)
	}
	if err := validateDungeonEntry(dnfrepo.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 8}}, dungeon, nil); err != nil {
		t.Fatalf("eight fatigue admission err=%v", err)
	}
	if err := validateDungeonEntry(dnfrepo.CharacterRecord{Level: 90, Stats: map[string]int64{"fatigue": 100}}, worldmap.Dungeon{ID: 8500}, nil); err == nil {
		t.Fatal("guild dungeon without its integer declaration was admitted")
	}

	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	accountID := "ordinary-account"
	_ = repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID})
	_ = repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: accountID, Stats: map[string]int64{"fatigue": 20}})
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{accountID: accountID, selectedCharacterID: 77}
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 1}}
	for _, coordinate := range []worldmap.RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}, {X: 1, Y: 1}} {
		if err := s.debitDungeonRoomFatigue(session, runtime, worldmap.DungeonRoomScene{Coordinate: coordinate}); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.Character.Stats["fatigue"] != 16 {
		t.Fatalf("ordinary room fatigue=%d", runtime.Character.Stats["fatigue"])
	}
}

func TestGuildDungeonFatigueDailyAssetsAndFailureBoundary(t *testing.T) {
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	const accountID = "daily-fatigue-account"
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{accountID: accountID, selectedCharacterID: 99}
	dungeon := guildDungeonFatigueTestDungeon()
	assets, err := s.currentDailyCountEntryAssets(session, dungeon, true)
	if err != nil {
		t.Fatal(err)
	}
	character := dnfrepo.CharacterRecord{CharacterID: "99", AccountID: accountID, Stats: map[string]int64{"fatigue": 20}}
	if err := assets.inTransaction(&character); err != nil {
		t.Fatal(err)
	}
	if !assets.debitRoomFatigue || assets.fatigueCost != 8 || !assets.fatigueStartOnly || character.Stats["fatigue"] != 12 || assets.fatigueSpent == nil || *assets.fatigueSpent != 8 {
		t.Fatalf("daily assets=%+v fatigue=%d", assets, character.Stats["fatigue"])
	}

	runtime := &runtimeDungeonState{Dungeon: dungeon}
	scene := worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0}}
	if !currentDailyCountFatiguePending(runtime, scene, true) {
		t.Fatal("fresh run did not need entry fatigue")
	}
	runtime.fatigueStartDebited = true
	if currentDailyCountFatiguePending(runtime, scene, true) {
		t.Fatal("committed guild start fatigue was pending again")
	}

	insufficient := dnfrepo.CharacterRecord{Stats: map[string]int64{"fatigue": 7}}
	if err := debitCurrentDungeonFatigueAmount(&insufficient, 8); !errors.Is(err, dnfdungeon.ErrEntryFatigueExhausted) {
		t.Fatalf("insufficient debit err=%v", err)
	}
	if insufficient.Stats["fatigue"] != 7 {
		t.Fatalf("failed debit mutated fatigue=%d", insufficient.Stats["fatigue"])
	}
}
