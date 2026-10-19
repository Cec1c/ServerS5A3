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
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// P162 boundary overlay. These tests target only the dungeon-entry clock edge:
// closed-day commit failure, successful commit, duplicate commit, confirmed
// town return, and the party-follower rollback path. They do not change the
// 60/360 second rule or any feeding behavior.
func TestPetEntryBoundaryFailureSuccessAndDuplicateCommit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires the fixed runtime Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	adventure, err := adventuregroup.LoadComplete(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "31",
		AccountID:   "p162-boundary-account",
		Level:       90,
		Stats:       map[string]int64{"town_id": 107, "area_id": 0, "pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3, "fatigue": 100},
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: character.AccountID}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	service := &Service{
		options:                 options{currentClientVersion: "official-24.9", pvfPath: path, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		initialEquipmentArchive: archive,
		adventureGroupTable:     adventure,
		gameplayTimers:          queue,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	session := &gameSession{selectedCharacterID: 31, accountID: character.AccountID, conn: &bufferConn{}, connID: "p162-entry-boundary"}
	bindDungeonSelectorOriginForTestAt(t, service, session, 107, 0, 250, 224)
	runtime, _, err := service.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 4907, Difficulty: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	session.petGrowth = petGrowthClockState{
		mode:        currentPetGrowthClockTown,
		characterID: 31,
		anchor:      queue.now,
		generation:  7,
	}

	// The prepared run crosses into a closed weekday. This is a real commit
	// rejection after preparation, not a pre-lock validation shortcut.
	queue.now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := service.commitDungeonRuntime(session, runtime); !errors.Is(err, dnfdungeon.ErrDayDungeonClosed) {
		t.Fatalf("closed-day commit error = %v, want %v", err, dnfdungeon.ErrDayDungeonClosed)
	}
	if session.petGrowth.mode != currentPetGrowthClockTown {
		t.Fatalf("closed-day commit changed pet clock to %s, want %s", session.petGrowth.mode, currentPetGrowthClockTown)
	}
	if !session.petGrowth.anchor.Equal(queue.now) && !session.petGrowth.anchor.Equal(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("closed-day commit moved pet clock anchor to %s", session.petGrowth.anchor)
	}
	if session.petGrowth.generation != 7 {
		t.Fatalf("closed-day commit advanced pet generation to %d, want 7", session.petGrowth.generation)
	}
	if session.dungeon.runtime != nil {
		t.Fatal("closed-day rejection installed a dungeon runtime")
	}

	// Reopen the weekday. A pet tick and the successful entry commit are run
	// concurrently to exercise the current callback/commit lock order.
	queue.now = time.Date(2026, 10, 8, 12, 0, 1, 0, time.UTC)
	tickDone := make(chan struct{})
	go func() {
		service.runCurrentPetGrowthTick(session, 7)
		close(tickDone)
	}()
	if err := service.commitDungeonRuntime(session, runtime); err != nil {
		t.Fatalf("open-day commit failed: %v", err)
	}
	select {
	case <-tickDone:
	case <-time.After(15 * time.Second):
		t.Fatal("pet growth tick blocked behind the entry commit")
	}
	if session.petGrowth.mode != currentPetGrowthClockDungeon {
		t.Fatalf("successful commit left pet clock in %s, want %s", session.petGrowth.mode, currentPetGrowthClockDungeon)
	}
	if !session.petGrowth.anchor.Equal(queue.now) {
		t.Fatalf("successful commit anchor = %s, want %s", session.petGrowth.anchor, queue.now)
	}
	if session.petGrowth.generation != 8 {
		t.Fatalf("successful commit generation = %d, want 8", session.petGrowth.generation)
	}
	anchor := session.petGrowth.anchor
	generation := session.petGrowth.generation

	// A duplicate commit must be rejected before the clock edge runs.
	if err := service.commitDungeonRuntime(session, runtime); !errors.Is(err, errDungeonAlreadyActive) {
		t.Fatalf("duplicate commit error = %v, want %v", err, errDungeonAlreadyActive)
	}
	if session.petGrowth.mode != currentPetGrowthClockDungeon {
		t.Fatalf("duplicate commit changed pet clock to %s", session.petGrowth.mode)
	}
	if !session.petGrowth.anchor.Equal(anchor) || session.petGrowth.generation != generation {
		t.Fatalf("duplicate commit re-anchored pet clock: anchor %s->%s generation %d->%d", anchor, session.petGrowth.anchor, generation, session.petGrowth.generation)
	}
}

func TestPetEntryBoundaryConfirmedTownReturnRestoresTownClock(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires the fixed runtime Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service, session, runtime := newBackToVillageRuntime(t)
	service.initialEquipmentArchive = archive
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	service.gameplayTimers = queue

	// newBackToVillageRuntime commits the legal dungeon route. The P162 patch
	// should therefore have armed the dungeon clock; normalize the anchor so the
	// settlement is deterministic.
	session.petGrowth.mu.Lock()
	session.petGrowth.mode = currentPetGrowthClockDungeon
	session.petGrowth.characterID = 99
	session.petGrowth.anchor = queue.now
	session.petGrowth.generation++
	session.petGrowth.timerName = ""
	session.petGrowth.mu.Unlock()
	queue.now = queue.now.Add(90 * time.Second)

	if err := service.handleDungeonBackToVillage(session, nil); err != nil {
		t.Fatalf("back-to-village request failed: %v", err)
	}
	if session.petGrowth.mode != currentPetGrowthClockDungeon {
		t.Fatalf("unconfirmed return changed pet clock to %s", session.petGrowth.mode)
	}
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "p162_boundary_return"); err != nil {
		t.Fatalf("confirmed town return failed: %v", err)
	}
	if runtime != nil && session.dungeon.runtime != nil {
		t.Fatal("confirmed town return retained the dungeon runtime")
	}
	if session.petGrowth.mode != currentPetGrowthClockTown {
		t.Fatalf("confirmed town return left pet clock in %s, want %s", session.petGrowth.mode, currentPetGrowthClockTown)
	}
	if session.petGrowth.characterID != 99 {
		t.Fatalf("confirmed town return character = %d, want 99", session.petGrowth.characterID)
	}
	if !session.petGrowth.anchor.Equal(queue.now) {
		t.Fatalf("confirmed town return anchor = %s, want %s", session.petGrowth.anchor, queue.now)
	}
}

// A rollback of the matching follower runtime must restore town recovery.
// A separate regression covers deferred group preparation before activation.
func TestPetEntryBoundaryPartyRollbackRestoresTownClock(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	if session.petGrowth.mode != currentPetGrowthClockDungeon {
		t.Fatalf("precondition: pet clock = %s, want %s", session.petGrowth.mode, currentPetGrowthClockDungeon)
	}
	// This fixture has no pet PVF. Keep the rollback at the entry boundary so
	// a real-clock delta cannot turn this mode-only test into elapsed settlement.
	queue := newFakeCurrentDungeonDeathTimerQueue()
	session.petGrowth.mu.Lock()
	queue.now = session.petGrowth.anchor
	session.petGrowth.mu.Unlock()
	service.gameplayTimers = queue
	service.rollbackRuntimePartyPreparedFollowers([]*runtimePartyPreparedDungeonEntry{
		{session: session, runtime: runtime, committed: true},
	})
	if session.dungeon.runtime != nil {
		t.Fatal("rollback did not clear the prepared follower runtime")
	}
	if session.petGrowth.mode != currentPetGrowthClockTown {
		t.Fatalf("party rollback left pet clock in %s, want %s", session.petGrowth.mode, currentPetGrowthClockTown)
	}
}
