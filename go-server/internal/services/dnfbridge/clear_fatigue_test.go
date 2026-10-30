package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func TestClearFatigueTracksCommittedDebitsAcrossRefills(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 1, 21, 59, 0, 0, time.UTC)
	c := repo.CharacterRecord{CharacterID: "99", AccountID: "account-1", Stats: map[string]int64{"fatigue": 1, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(queue.Now())}}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1"}, gameplayTimers: queue, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 99}
	run := &runtimeDungeonState{}
	debit := func(x int) {
		t.Helper()
		if err := s.debitDungeonRoomFatigue(session, run, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: int64(x)}}); err != nil {
			t.Fatal(err)
		}
	}
	debit(0)
	debit(0)
	debit(1)
	if run.fatigueSpent != 1 {
		t.Fatalf("last point and revisit: %d", run.fatigueSpent)
	}
	// A potion changes the balance between debits, not the per-run counter.
	c, _, _ = repos.Character.Load(ctx, "99")
	c.Stats["fatigue"] = 10
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	debit(2)
	if run.fatigueSpent != 2 {
		t.Fatalf("potion: %d", run.fatigueSpent)
	}
	queue.now = queue.now.Add(2 * time.Minute)
	debit(3)
	if run.fatigueSpent != 3 || run.Character.Stats["fatigue"] != 155 {
		t.Fatalf("daily refresh: spent=%d balance=%d", run.fatigueSpent, run.Character.Stats["fatigue"])
	}
	repos.CharacterAssets = failedClearFatigueAssets{repos.CharacterAssets}
	if err := s.debitDungeonRoomFatigue(session, run, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 4}}); err == nil {
		t.Fatal("expected rollback")
	}
	c, _, _ = repos.Character.Load(ctx, "99")
	if run.fatigueSpent != 3 || c.Stats["fatigue"] != 155 || run.fatigueRooms[worldmap.RoomCoordinate{X: 4}] {
		t.Fatal("rolled-back debit counted")
	}
	repos.CharacterAssets = memory.NewMemoryGroup().CharacterAssets // no-fatigue must not reach a repository
	run.Dungeon.Metadata.NoFatigue = true
	debit(5)
	if run.fatigueSpent != 3 {
		t.Fatal("free room counted")
	}
}

type failedClearFatigueAssets struct{ repo.CharacterAssetUnitOfWork }

func (f failedClearFatigueAssets) WithinCharacterAssets(ctx context.Context, id string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return f.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(c, i, e); err != nil {
			return err
		}
		return errors.New("fixture rollback")
	})
}

func TestClearFatigueGuildReplayCountsEachMemberOnce(t *testing.T) {
	a, b := &runtimeDungeonState{}, &runtimeDungeonState{}
	members := []guildDungeonEntryFatigueMember{{session: &gameSession{selectedCharacterID: 1}, runtime: a}, {session: &gameSession{selectedCharacterID: 2}, runtime: b}}
	markGuildDungeonEntryFatigueCommitted(members, nil, 8)
	markGuildDungeonEntryFatigueCommitted(members, nil, 8)
	if a.fatigueSpent != 8 || b.fatigueSpent != 8 {
		t.Fatalf("guild replay: %d %d", a.fatigueSpent, b.fatigueSpent)
	}
}
