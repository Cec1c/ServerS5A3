package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func realAntonConstructorRuntimeFixture(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, *runtimeRaidBattle, uint32, time.Time) {
	t.Helper()
	source, _, _, _, _ := actualAntonRandomizedBridgeFixture(t)
	a := source.initialEquipmentArchive
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-180 * time.Second)
	progress, err := program.BeginPhase(1, base, 19871)
	if err != nil {
		t.Fatal(err)
	}
	progress, err = progress.Tick(base.Add(180 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	id := int64(progress.InfectionDungeon())
	dungeon, ok := table.FindDungeon(id)
	if !ok {
		t.Fatal("actual infected hatchery missing")
	}
	topology, err := worldmap.BuildDungeonLayout(resolver, id, 0, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, actualRoom := range topology.Rooms() {
		if actualRoom.Map == nil {
			continue
		}
		for _, spawn := range actualRoom.Map.Map.Monsters {
			definition, found, err := source.dungeonMonsterTable.Find(spawn.MonsterID)
			if err != nil {
				t.Fatal(err)
			}
			if !found || !strings.HasPrefix(strings.ToLower(definition.Path), "monster/newmonsters/anton/phase3/constructor/constructor") {
				continue
			}
			// Explicit in-memory entry fixture at an actual PVF room. This is
			// not represented as a client walking through the hatchery.
			coord := actualRoom.Coordinate
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
			s, session, runtime, b := raidLifeRuntimeFixture(t)
			runtime.Dungeon, runtime.Session, runtime.Room = dungeon, ds, room
			runtime.raidEntry.Phase, runtime.raidEntry.Attempt = 1, progress.Epoch()
			b.Phase, b.Anton, b.Dungeons = 1, progress, progress.Dungeons()
			b.EnterAfter, b.Deadline = base, base.Add(time.Hour)
			for _, monster := range room.Snapshot().Monsters {
				if monster.Definition.Path == definition.Path {
					return s, session, runtime, b, monster.ObjectKey, base.Add(180 * time.Second)
				}
			}
			t.Fatal("actual constructor omitted from runtime")
		}
	}
	t.Fatal("actual infected hatchery has no map-owned constructor")
	return nil, nil, nil, nil, 0, time.Time{}
}

func TestRealPVFAntonConstructorAcceptedRoomDeathBarrierCaller(t *testing.T) {
	s, session, runtime, b, key, at := realAntonConstructorRuntimeFixture(t)
	if _, err := s.recordCurrentAntonConstructorDeathLocked(session, runtime, key, at); err == nil {
		t.Fatal("living actor changed shield")
	}
	if _, _, err := runtime.Room.CommitActorDeathReport(key, runtime.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recordCurrentAntonConstructorDeathLocked(session, runtime, key, at); err == nil {
		t.Fatal("unsent death changed shield")
	}
	response, err := buildCurrentDungeonDeathNotificationBodyWithDrops(key, currentDungeonDeathResponseMonster, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Room.RecordDeathResponseBody(key, response)
	states, err := s.recordCurrentAntonConstructorDeathLocked(session, runtime, key, at)
	if err != nil || len(states) != 1 || b.Anton.NativeSymbols()[110] != 8500 {
		t.Fatal("actual infected constructor caller", len(states), err, b.Anton.NativeSymbols()[110])
	}
	version := b.ProgressVersion
	states, err = s.recordCurrentAntonConstructorDeathLocked(session, runtime, key, at)
	if err != nil || len(states) != 0 || b.ProgressVersion != version || b.Anton.NativeSymbols()[110] != 8500 {
		t.Fatal("duplicate accepted constructor counted twice", err)
	}
}

func TestRealPVFAntonConstructorRejectsForeignAndExpiredRoomDeaths(t *testing.T) {
	s, session, runtime, b, key, at := realAntonConstructorRuntimeFixture(t)
	if _, _, err := runtime.Room.CommitActorDeathReport(key, runtime.Session); err != nil {
		t.Fatal(err)
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(key, currentDungeonDeathResponseMonster, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Room.RecordDeathResponseBody(key, body)
	baseline := cloneRuntimeRaidBattle(b)
	entry := *runtime.raidEntry
	dungeonID := runtime.Dungeon.ID
	accountID, channelID := runtime.AccountID, session.channel.ID
	for _, name := range []string{"old_runtime", "wrong_hatch", "account", "login", "channel", "raid_key", "instance", "phase", "attempt", "empty_run", "participant", "cleared_run", "single", "mode", "no_program", "completed", "timeout", "expired", "before_entry", "backwards", "zero_time", "town_return", "closed_hatch"} {
		t.Run(name, func(t *testing.T) {
			*b = *cloneRuntimeRaidBattle(baseline)
			copyEntry := entry
			runtime.raidEntry = &copyEntry
			runtime.Dungeon.ID, runtime.AccountID = dungeonID, accountID
			runtime.townReturnPending = false
			session.dungeon.runtime, session.channel.ID, session.loginPacketSeen = runtime, channelID, true
			now := at
			switch name {
			case "old_runtime":
				session.dungeon.runtime = nil
			case "wrong_hatch":
				runtime.Dungeon.ID = 221
			case "account":
				runtime.AccountID = "foreign"
			case "login":
				session.loginPacketSeen = false
			case "channel":
				session.channel.ID++
			case "raid_key":
				runtime.raidEntry.RaidKey++
			case "instance":
				runtime.raidEntry.InstanceID = "foreign"
			case "phase":
				runtime.raidEntry.Phase = 0
			case "attempt":
				runtime.raidEntry.Attempt++
			case "empty_run":
				runtime.raidEntry.RunID = ""
			case "participant":
				runtime.raidEntry.Participants = []uint16{100}
			case "cleared_run":
				b.ClearedRuns = map[string]currentRaidClearedRun{entry.RunID: {}}
			case "single":
				b.Single = true
			case "mode":
				b.Mode = 2
			case "no_program":
				b.Anton = nil
			case "completed":
				b.PhaseCompleted = true
			case "timeout":
				b.TimedOut = true
			case "expired":
				b.Deadline = now
			case "before_entry":
				b.EnterAfter = now.Add(time.Second)
			case "backwards":
				now = now.Add(-time.Second)
			case "zero_time":
				now = time.Time{}
			case "town_return":
				runtime.townReturnPending = true
			case "closed_hatch":
				next, _, e := b.Anton.ClearDungeon("other-owned-clear", uint32(dungeonID), b.Anton.Epoch(), at)
				if e != nil {
					t.Fatal(e)
				}
				b.Anton, b.Dungeons = next, next.Dungeons()
			}
			if _, e := s.recordCurrentAntonConstructorDeathLocked(session, runtime, key, now); e == nil {
				t.Fatal("unowned event accepted")
			}
			if b.Anton != nil && b.Anton.NativeSymbols()[110] != 10000 {
				t.Fatal("rejected event reduced shield")
			}
		})
	}
}

func TestRealPVFAntonConstructorCurrentOp39AcceptedDeathConnectsBarrier(t *testing.T) {
	s, session, runtime, b, key, _ := realAntonConstructorRuntimeFixture(t)
	body := make([]byte, 88)
	binary.LittleEndian.PutUint32(body[0:4], key)
	binary.LittleEndian.PutUint16(body[4:6], 99)
	body[30] = 1
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Room.DeathResponseBody(key); !ok || b.Anton.NativeSymbols()[110] != 8500 {
		t.Fatal("accepted native death failed to reach actual barrier rule", b.Anton.NativeSymbols()[110])
	}
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	if b.Anton.NativeSymbols()[110] != 8500 {
		t.Fatal("native death replay reduced shield twice")
	}
}
