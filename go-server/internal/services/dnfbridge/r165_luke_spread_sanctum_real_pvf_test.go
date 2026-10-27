package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// prepareR165LukeSpreadSanctumRealEntryFixture enters the real 5056 map without
// synthesizing a clear. The 5054 prior entry is resource-VM history setup.
func prepareR165LukeSpreadSanctumRealEntryFixture(t *testing.T) (*raidStartGuardFixture, *gameSession) {
	t.Helper()
	f, p, _ := normalLukeFixture(t)
	s := f.service
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, s.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(currentLukeSpreadSanctumDungeonID)
	if !ok || dungeon.Path != "dungeon/Luke/Phase1/Sanctum_of_Spread.dgn" {
		t.Fatalf("real 5056 path=%q found=%t", dungeon.Path, ok)
	}
	if len(dungeon.Mazes) != 15 {
		t.Fatalf("real 5056 maze count=%d want 15", len(dungeon.Mazes))
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
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	character, found, err := f.repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	character.Level = 90
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["fatigue"] = 156
	character.Stats["town_id"] = 50
	character.Stats["area_id"] = 0
	character.Stats["pos_x"] = 580
	character.Stats["pos_y"] = 243
	character.Stats["direction"] = 0
	character.Stats["area_state"] = 0
	if err := f.repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	// Real luke.etc: entering 5054 opens 5055/5056. No prior clear is fabricated:
	// clearing before a maze group exists correctly fails the production gate.
	progress := &raid.LukeProgression{AntonProgression: f.state.Battle.Anton}
	at := time.Now().Add(-time.Minute)
	progress, _, err = progress.EnterDungeon("r165-prior-birth", 5054, progress.Epoch(), at)
	if err != nil {
		t.Fatal("real 5054 entry history: ", err)
	}
	opened := false
	for _, state := range progress.Dungeons() {
		if state.ID == uint32(currentLukeSpreadSanctumDungeonID) && state.State == 0 {
			opened = true
		}
	}
	if !opened {
		t.Fatalf("real 5056 still closed after 5054 history: %+v", progress.Dungeons())
	}
	f.state.Battle.Anton = progress.AntonProgression
	f.state.Battle.Dungeons = progress.Dungeons()

	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: uint32(currentLukeSpreadSanctumDungeonID), Difficulty: 2})
	if err := s.handleDungeonSelectUpper(p, body); err != nil {
		t.Fatal(err)
	}
	r := p.dungeon.runtime
	if r == nil || r.Dungeon.ID != currentLukeSpreadSanctumDungeonID || r.raidEntry == nil || r.raidEntry.Phase != 0 || !r.raidEntry.teamMode {
		t.Fatalf("real 5056 entry runtime=%+v logs=%s", r, f.logText(t))
	}
	if r.Session == nil || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("real 5056 entry did not produce an active run")
	}
	return f, p
}

// prepareR165LukeSpreadSanctumRealResultFixture produces an accepted clear
// through the actual bridge reducer. Room completion and boss barriers are
// explicit test fixtures; target entry, clear reducer and op72 use production
// handlers.
func prepareR165LukeSpreadSanctumRealResultFixture(t *testing.T) (*raidStartGuardFixture, *gameSession) {
	t.Helper()
	f, p := prepareR165LukeSpreadSanctumRealEntryFixture(t)
	s, r := f.service, p.dungeon.runtime
	if err := r.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
	state, changed, err := s.reduceCurrentRaidDungeonClearLocked(p, r, time.Now())
	if err != nil || !changed {
		t.Fatalf("real 5056 accepted clear changed=%t err=%v", changed, err)
	}
	cleared, found := state.Battle.ClearedRuns[r.raidEntry.RunID]
	if !found || cleared.DungeonID != uint32(currentLukeSpreadSanctumDungeonID) || cleared.Phase != 0 || cleared.Attempt != r.raidEntry.Attempt {
		t.Fatalf("real 5056 clear journal receipt=%+v found=%t", cleared, found)
	}
	if r.settlementClearRewardSent {
		t.Fatal("real 5056 result page unexpectedly has the ordinary op35 reward marker")
	}
	return f, p
}

// The reported repeat was tied to 5056. Use the real Luke resource and the
// actual accepted-clear reducer, then make the cleared member's occupancy row
// disappear and return. The native 584/op4 event must remain a single edge.
func TestRealPVFLukeSpreadSanctumClearNoticeDoesNotReplayAfterReentry(t *testing.T) {
	f, p := prepareR165LukeSpreadSanctumRealEntryFixture(t)
	s, r := f.service, p.dungeon.runtime
	p.postStartMapPlayerStateSent = true
	s.recordCurrentRaidParticipation(p, r)
	b := f.state.Battle
	entry, occupied := b.Participation[p.selectedCharacterID]
	if !occupied || entry.RunID == "" {
		t.Fatal("real 5056 entry did not establish participation")
	}
	collect := func() int {
		t.Helper()
		f.conn.write.Reset()
		if err := s.sendCurrentRaidProgressProjection(p, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		stream := f.conn.write.Bytes()
		count := 0
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.MsgID == 584 && len(packet.Body) > 5 && packet.Body[5] == 4 {
				count++
			}
		}
		return count
	}
	if got := collect(); got != 0 {
		t.Fatalf("entry announced historical clear %d times", got)
	}
	if err := r.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
	if _, changed, err := s.reduceCurrentRaidDungeonClearLocked(p, r, time.Now()); err != nil || !changed {
		t.Fatalf("real 5056 accepted clear changed=%t err=%v", changed, err)
	}
	if got := collect(); got != 1 {
		t.Fatalf("first accepted 5056 clear notices=%d want=1", got)
	}

	delete(b.Participation, p.selectedCharacterID)
	b.ProgressVersion++
	if got := collect(); got != 0 {
		t.Fatalf("5056 clear replayed on departure %d times", got)
	}
	b.Participation[p.selectedCharacterID] = entry
	b.ProgressVersion++
	if got := collect(); got != 0 {
		t.Fatalf("same 5056 accepted clear replayed after re-entry %d times", got)
	}
}

// The old result-page gate failed on the missing ordinary settlement flag. The
// narrow op72 owner must accept the real 5056 clear journal, then leave retry,
// selector, other sanctums and Anton on the existing boundary.
func TestRealPVFR165LukeSpreadSanctumResultOp72UsesClearJournal(t *testing.T) {
	f, p := prepareR165LukeSpreadSanctumRealResultFixture(t)
	s, r := f.service, p.dungeon.runtime
	runID := r.raidEntry.RunID
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	if live == nil || live.Battle == nil {
		s.raidMu.Unlock()
		t.Fatal("live raid aggregate missing")
	}
	saved := live.Battle.ClearedRuns[runID]
	if saved.DungeonID != uint32(currentLukeSpreadSanctumDungeonID) || saved.Phase != 0 || saved.Attempt != r.raidEntry.Attempt {
		s.raidMu.Unlock()
		t.Fatalf("fixture clear journal receipt changed before negative control: %+v", saved)
	}
	foreign := saved
	foreign.Attempt++
	live.Battle.ClearedRuns[runID] = foreign
	s.raidMu.Unlock()

	f.conn.write.Reset()
	if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	rejectedFrames := splitUpperFrames(t, f.conn.write.Bytes())
	if p.dungeon.runtime != r || r.settlementCardExitAckSent || r.townReturnOp24Sent ||
		r133RegressionCountMsg(rejectedFrames, 72) != 0 || r133RegressionCountMsg(rejectedFrames, 24) != 0 {
		t.Fatalf("foreign clear attempt opened result-page return ack=%t op24=%t bytes=%x", r.settlementCardExitAckSent, r.townReturnOp24Sent, f.conn.write.Bytes())
	}

	s.raidMu.Lock()
	live.Battle.ClearedRuns[runID] = saved
	s.raidMu.Unlock()
	f.conn.write.Reset()
	if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != nil || !r.townReturnOp24Sent {
		t.Fatalf("real 5056 result page did not return runtime=%p op24=%t", p.dungeon.runtime, r.townReturnOp24Sent)
	}
	if r.settlementClearRewardSent {
		t.Fatal("real 5056 return manufactured an ordinary clear reward")
	}
	frames := splitUpperFrames(t, f.conn.write.Bytes())
	if r133RegressionCountMsg(frames, 72) != 1 || r133RegressionCountMsg(frames, 24) != 1 {
		t.Fatalf("real 5056 result page frames=%d op72=%d op24=%d", len(frames), r133RegressionCountMsg(frames, 72), r133RegressionCountMsg(frames, 24))
	}
}
