package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestLukeClearHistoryControlsRepeatLoot(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	service := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }}
	d := worldmap.Dungeon{ID: 5101, Path: "dungeon/luke_normal/a.dgn"}
	at := time.Date(2026, 11, 17, 21, 59, 59, 0, time.UTC)
	check := func(character string, dungeon worldmap.Dungeon, at time.Time, want bool) {
		t.Helper()
		got, err := service.lukeRepeatEntry(ctx, character, dungeon, at)
		if got != want || err != nil {
			t.Fatal(got, want, err)
		}
	}
	check("19", d, at, false)
	err := g.Character.(repo.ClearHistoryWriter).AppendClear(ctx, moderation.Clear{CharacterID: "19", AccountID: "account-1", Key: "clear", DungeonID: 5101, FinishedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	check("19", d, at, true)
	check("19", d, at.Add(time.Second), false)
	check("20", d, at, false)
	d.ID++
	check("19", d, at, false)
	s, r, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
	r.lukeRepeat = true
	p := &gameSession{selectedCharacterID: 19}
	p.dungeon.runtime = r
	drops, err := s.planCurrentDungeonMonsterDrops(r, r.Room.Snapshot().Monsters[0], currentSceneActorObjectKey(19))
	if err != nil || len(drops) != 0 {
		t.Fatal(drops, err)
	}
	if err := s.freezeCurrentDungeonCardRewardStateLocked(p, r); err != nil {
		t.Fatal(err)
	}
	for _, side := range r.settlementCardRewardState.plan.Sides {
		if side.Gold != 0 || len(side.Items) != 0 || side.Cost != 0 || side.ConsumePremiumDaily {
			t.Fatal(side)
		}
	}
	for _, side := range []dungeonCardSide{dungeonCardSideFree, dungeonCardSidePaid} {
		_, _, project, err := s.grantCurrentDungeonSelectedCardAssetsLocked(p, r, side, 0)
		if err != nil || project || !r.settlementCardRewardState.delivered[side] {
			t.Fatalf("repeat card side %v: project=%v err=%v", side, project, err)
		}
	}
	if r.settlementCardRewardState.plan.BlackDiamondBonus.Gold != 0 || len(r.settlementCardRewardState.plan.BlackDiamondBonus.Items) != 0 {
		t.Fatal("repeat bonus")
	}
}

func TestRealPVFSingleLukeReviveConsumesOnce(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeCoinFixtureForDungeonWithSetup(t, false, 5323, func(f *raidStartGuardFixture, s *gameSession) {
		b := f.state.Battle
		b.Single = true
		b.Anton = nil
		b.SingleSequence = []uint32{5323, 5324, 5325, 5326, 5327, 5328}
		b.Dungeons = nil
		for i, id := range b.SingleSequence {
			state := byte(1)
			if i == 0 {
				state = 0
			}
			b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: id, State: state})
		}
		f.state.RouteOrRaidType = 1
		f.state.CurrentUIFlags = [3]byte{1, 0, 0}
	})
	if !r.raidEntry.singleLukeMode {
		t.Fatal("missing single entry")
	}
	f.service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	r.lifecycleToken = 1
	before := reviewLukeCoinBalance(t, f, s)
	reviewLukeCoinDie(t, f, s)
	if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
		t.Fatal(err)
	}
	if r.deathReturnWaiting || reviewLukeCoinBalance(t, f, s) != before-1 {
		t.Fatal("valid single revive failed")
	}
	if err := f.service.handleDungeonUseCoin(s, reviewLukeCoinRequest(s)); err != nil {
		t.Fatal(err)
	}
	if reviewLukeCoinBalance(t, f, s) != before-1 {
		t.Fatal("duplicate charge")
	}
}
