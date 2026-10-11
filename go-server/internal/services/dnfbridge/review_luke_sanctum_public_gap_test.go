package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

// Draft: actual resource prepare+public op16/commit and accepted clear reducer.
// Completion/boss barriers are explicit test-fixture state; no actual fight claim.
// Initial chooser0 is a fixture selecting the first legitimate group, not a daily policy.
func TestRealPVFLukePublicSanctumAcceptedClearAdvancesMazeTriplet(t *testing.T) {
	f, p, _ := normalLukeFixture(t)
	s := f.service
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, s.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable = table
	s.worldMapResolver, err = worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonAICharacterTable, err = newPVFDungeonAICharacterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonChoice = func(limit int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	char, found, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	char.Level = 90
	if char.Stats == nil {
		char.Stats = map[string]int64{}
	}
	char.Stats["fatigue"] = 156
	char.Stats["town_id"] = 50
	char.Stats["area_id"] = 0
	char.Stats["pos_x"] = 580
	char.Stats["pos_y"] = 243
	char.Stats["direction"], char.Stats["area_state"] = 0, 0
	if err = f.repos.Character.Save(ctx, char); err != nil {
		t.Fatal(err)
	}
	for ordinal := 0; ordinal < 3; ordinal++ {
		body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: 5054, Difficulty: 2})
		if err = s.handleDungeonSelectUpper(p, body); err != nil {
			t.Fatalf("public entry ordinal%d: %v", ordinal, err)
		}
		r := p.dungeon.runtime
		if r == nil || r.Dungeon.ID != 5054 || r.raidEntry == nil {
			t.Fatalf("public entry refused ordinal%d", ordinal)
		}
		t.Logf("actual prepare committed ordinal=%d maze=%d frozenrun=%s", ordinal, r.MazeIndex, r.raidEntry.RunID)
		if r.MazeIndex != ordinal {
			t.Fatalf("accepted clear ordinal%d still chose maze%d; first-group resource triplet requires %d", ordinal, r.MazeIndex, ordinal)
		}
		if err = r.Session.CompleteCurrentRoom(); err != nil {
			t.Fatal(err)
		}
		r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
		state, changed, err := s.reduceCurrentRaidDungeonClearLocked(p, r, time.Now())
		if err != nil || !changed || len(state.Battle.ClearedRuns) != ordinal+1 {
			t.Fatalf("real accepted clear ordinal%d changed=%v err=%v", ordinal, changed, err)
		}
		// Explicit completed-result fixture barriers. No asset grant or physical result claim.
		r.settlementClearRewardSent = true
		r.settlementPlayResultReceived, r.settlementStatisticReceived = true, true
		r.advanceSettlementPhase(currentDungeonSettlementPhaseResultShown)
		if !s.currentRaidCompletedMapExitReady(p, r) {
			t.Fatal("completed-result fixture not ready")
		}
		if err = s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
			t.Fatal(err)
		}
		if p.dungeon.runtime != nil || !r.townReturnOp24Sent {
			t.Fatal("completed owned runtime failed normal town return")
		}
	}
}
