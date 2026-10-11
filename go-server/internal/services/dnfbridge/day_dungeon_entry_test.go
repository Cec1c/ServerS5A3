package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFDayDungeonPreparedCommitAndActiveRunRetry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	adventure, err := adventuregroup.LoadComplete(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	repos := memory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "31", AccountID: "day-prepared-account", Level: 90, Stats: map[string]int64{"town_id": 107, "area_id": 0, "pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3, "fatigue": 100}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: character.AccountID, Metadata: map[string]string{"unrelated": "retain"}}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := &Service{options: options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain}, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters, initialEquipmentArchive: a, adventureGroupTable: adventure, gameplayTimers: queue, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 31, accountID: character.AccountID, conn: &bufferConn{}, connID: "day-prepared"}
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	runtime, _, err := s.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 4907, Difficulty: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	owner, cmd, err := s.currentDayDungeonEntryCommand(ctx, runtime.Character, runtime.Dungeon, session)
	if err != nil {
		t.Fatal(err)
	}
	status, err := owner.Status(ctx, cmd)
	if err != nil || status.CharacterUsed != 0 || status.AccountCharactersUsed != 0 {
		t.Fatalf("preparation charged quota: %+v %v", status, err)
	}
	// A prepared runtime that crosses into a closed weekday cannot commit.
	queue.now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := s.commitDungeonRuntime(session, runtime); !errors.Is(err, dnfdungeon.ErrDayDungeonClosed) {
		t.Fatalf("closed-day commit accepted: %v", err)
	}
	queue.now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	status, err = owner.Status(ctx, cmd)
	if err != nil || status.CharacterUsed != 0 || status.AccountCharactersUsed != 0 || session.dungeon.runtime != nil {
		t.Fatalf("closed-day rejection charged quota or adopted runtime: %+v %v", status, err)
	}
	if err := s.commitDungeonRuntime(session, runtime); err != nil {
		t.Fatal(err)
	}
	if err := s.commitDungeonRuntime(session, runtime); !errors.Is(err, errDungeonAlreadyActive) {
		t.Fatalf("retry guard: %v", err)
	}
	stored, _, err := repos.Character.Load(ctx, "31")
	if err != nil {
		t.Fatal(err)
	}
	used, err := dnfdungeon.DailyEntryUsed(stored, 4907, queue.now)
	if err != nil || used != 1 || stored.Stats["fatigue"] != runtime.Character.Stats["fatigue"] {
		t.Fatalf("committed/retry count=%d err=%v", used, err)
	}
	if err := runtime.Session.Abandon(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 4907, Difficulty: 0}); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("second admission accepted: %v", err)
	}
	after, _, err := repos.Account.Load(ctx, character.AccountID)
	if err != nil || after.Metadata["unrelated"] != "retain" {
		t.Fatal("unrelated account metadata changed", err)
	}
	t.Log("actual 4907 map/monster preparation, no early charge, atomic commit, active retry rejected, subsequent daily limit preserved")
}
