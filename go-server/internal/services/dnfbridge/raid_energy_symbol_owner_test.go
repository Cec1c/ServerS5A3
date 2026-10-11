package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func actualEnergySymbolFixture(t *testing.T, large bool) (*Service, *gameSession, *runtimeDungeonState, *runtimeRaidBattle, time.Time) {
	t.Helper()
	source, dungeon, topology, _, _ := actualAntonRandomizedBridgeFixture(t)
	owner, err := source.prepareCurrentDungeonRandomizedOwner(dungeon, 0, topology, nil)
	if err != nil {
		t.Fatal(err)
	}
	var coord worldmap.RoomCoordinate
	found := false
	if large {
		for _, room := range topology.Rooms() {
			if room.Map == nil {
				continue
			}
			for _, object := range room.Map.Map.PassiveObjects {
				if object.ObjectID == 18864 {
					coord, found = room.Coordinate, true
				}
			}
		}
	} else {
		for _, entry := range owner.Entries {
			if entry.Source.ObjectID == 18865 && entry.SpawnMode == 1 && entry.Faction == 100 {
				coord, found = entry.Source.Map, true
				break
			}
		}
	}
	if !found {
		t.Fatal("actual selected transmission missing")
	}
	// An explicit in-memory committed room fixture, not a native walk-through.
	topology.Start = &coord
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	scene, _ := ds.Scene()
	room, _, err := newRuntimeDungeonRoom(scene, source.dungeonMonsterTable, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.AnnounceAllActors(ds); err != nil {
		t.Fatal(err)
	}
	s, session, r, b := raidLifeRuntimeFixture(t)
	r.Dungeon, r.Session, r.Room, r.MazeIndex, r.randomizedObjects = dungeon, ds, room, 0, owner
	session.postStartMapPlayerStateSent = true
	program, err := raid.LoadAntonProgram(source.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	progress, err := program.BeginPhase(1, at, 12345)
	if err != nil {
		t.Fatal(err)
	}
	r.raidEntry.Phase, r.raidEntry.Attempt = 1, progress.Epoch()
	b.Phase, b.Anton, b.Dungeons, b.EnterAfter, b.Deadline = 1, progress, progress.Dungeons(), at, at.Add(time.Hour)
	return s, session, r, b, at
}

func energySymbolBody(amount uint32) []byte {
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body, 110)
	binary.LittleEndian.PutUint32(body[4:], amount)
	body[8] = 2
	return body
}

func TestRealPVFEnergySymbolFrozenSmallSelectionAndSharedReplay(t *testing.T) {
	s, session, r, b, at := actualEnergySymbolFixture(t, false)
	before := b.Anton.NativeSymbols()[110]
	for _, body := range [][]byte{energySymbolBody(3000), energySymbolBody(2000), {110}, append(energySymbolBody(100), 0)} {
		if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, body, at); err == nil || changed || b.Anton.NativeSymbols()[110] != before {
			t.Fatal("foreign producer changed barrier", err, changed)
		}
	}
	if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(100), at); err != nil || !changed || b.Anton.NativeSymbols()[110] != before-100 {
		t.Fatal("actual frozen small producer refused", err, changed)
	}
	if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(100), at); err != nil || changed || b.Anton.NativeSymbols()[110] != before-100 {
		t.Fatal("same run room producer counted twice", err, changed)
	}
	r.randomizedObjects.Entries[0].Faction++
	if _, _, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(100), at); err == nil {
		t.Fatal("changed frozen namespace accepted")
	}
}

func TestRealPVFEnergyLargeSymbolRequiresNotifiedCompletedBossOwner(t *testing.T) {
	s, session, r, b, at := actualEnergySymbolFixture(t, true)
	if _, _, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(3000), at); err == nil {
		t.Fatal("live boss accepted large event")
	}
	for _, monster := range r.Room.Snapshot().Monsters {
		if monster.Definition.ID != 69311 {
			continue
		}
		if _, _, err := r.Room.CommitActorDeathReport(monster.ObjectKey, r.Session); err != nil {
			t.Fatal(err)
		}
		response, err := buildCurrentDungeonDeathNotificationBodyWithDrops(monster.ObjectKey, currentDungeonDeathResponseMonster, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Room.RecordDeathResponseBody(monster.ObjectKey, response)
	}
	if r.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
		if err := r.Session.CompleteCurrentRoom(); err != nil {
			t.Fatal(err)
		}
	}
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("accepted boss did not complete actual energy run")
	}
	if _, _, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(3000), at); err == nil {
		t.Fatal("missing raid clear receipt accepted")
	}
	e := r.raidEntry
	b.ClearedRuns = map[string]currentRaidClearedRun{e.RunID: {DungeonID: 218, Phase: e.Phase, Attempt: e.Attempt, Participants: append([]uint16(nil), e.Participants...)}}
	before := b.Anton.NativeSymbols()[110]
	if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(3000), at); err != nil || !changed || b.Anton.NativeSymbols()[110] != before-3000 {
		t.Fatal("exact late completed boss producer refused", err, changed)
	}
	if _, changed, err := s.recordCurrentAntonEnergySymbolLocked(session, energySymbolBody(3000), at); err != nil || changed {
		t.Fatal("completed large replay counted twice", err, changed)
	}
}
