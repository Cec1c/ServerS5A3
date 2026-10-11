package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestRealPVFAntonOpenMapRetryCommitsFreshOwnedEntry(t *testing.T) {
	f, session := realSingleAntonHPFixture(t, 0)
	s := f.service
	a := s.initialEquipmentArchive
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
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
	ai, err := newPVFDungeonAICharacterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable = table
	s.worldMapResolver = resolver
	s.dungeonMonsterTable = monsters
	s.dungeonAICharacterTable = ai
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	character, _, err := f.repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	character.Level = 90
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats["fatigue"] = 156
	character.Stats["town_id"] = 38
	character.Stats["area_id"] = 1
	character.Stats["pos_x"] = 450
	character.Stats["pos_y"] = 234
	character.Stats["direction"] = 5
	character.Stats["area_state"] = 3
	if err := f.repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	b := f.state.Battle
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := program.BeginPhase(0, b.StartedAt, 1234)
	if err != nil {
		t.Fatal(err)
	}
	b.Single = false
	b.SingleSequence = nil
	b.SingleHPSymbols = nil
	b.Configuration = config
	b.Anton = progress
	b.Dungeons = progress.Dungeons()
	old := raidCompletedRuntimeFixture(t, session, 210)
	old.Character = character
	old.Request.DungeonID = 210
	old.Request.RuntimeToken = 0xffff
	old.townReturnOrigin = currentTownPositionSnapshot{CharacterID: 19, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true}
	old.settlementPlayResultReceived = true
	old.settlementStatisticReceived = true
	old.settlementClearRewardSent = true
	old.settlementPhase = currentDungeonSettlementPhaseResultShown
	bindDungeonSelectorOriginForTestAt(t, s, session, 38, 1, 450, 234)
	if err := s.freezeCurrentRaidDungeonGroupEntry(session, old, nil); err != nil {
		t.Fatal(err)
	}
	b.ClearedRuns = map[string]currentRaidClearedRun{old.raidEntry.RunID: {DungeonID: 210, Phase: 0, Attempt: old.raidEntry.Attempt, Participants: []uint16{19}}}
	f.conn.write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	fresh := session.dungeon.runtime
	if fresh == nil || fresh == old || fresh.raidEntry == nil || fresh.raidEntry.InstanceID != b.InstanceID || fresh.raidEntry.RunID == old.raidEntry.RunID || fresh.Dungeon.ID != 210 || fresh.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("real PVF retry failed to commit fresh owned run", fresh)
	}
	stream := f.conn.write.Bytes()
	startMap := false
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == currentDungeonStartNotification {
			startMap = true
		}
		stream = rest
	}
	if !startMap {
		t.Fatal("fresh actual start-map not delivered")
	}
	if old.settlementCardRewardCommitted || old.settlementCardLayoutSent {
		t.Fatal("ordinary cards fabricated")
	}
}
