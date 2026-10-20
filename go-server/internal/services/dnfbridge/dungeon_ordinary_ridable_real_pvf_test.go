package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func ordinaryRidableRows(t *testing.T, body []byte) []currentDungeonStartMapRidableEntry {
	t.Helper()
	if len(body) < 23 || body[13] != currentDungeonStartMapPayloadBuild {
		t.Fatalf("expected full first-visit start map: %x", body)
	}
	offset := 19 + 21*int(body[18])
	if offset >= len(body) {
		t.Fatalf("truncated actors: %x", body)
	}
	if body[offset] != 0 {
		t.Fatal("ordinary fixture unexpectedly materialized item drops")
	}
	offset++ // No item rows; independent of the separate 20/21-byte item format.
	offset++ // hell-party fog
	if offset >= len(body) {
		t.Fatalf("truncated extras: %x", body)
	}
	groups := int(body[offset])
	offset++
	var rows []currentDungeonStartMapRidableEntry
	for group := 0; group < groups; group++ {
		if offset >= len(body) {
			t.Fatal("missing ridable group count")
		}
		count := int(body[offset])
		offset++
		for i := 0; i < count; i++ {
			if offset+20 > len(body) {
				t.Fatal("truncated ridable row")
			}
			rows = append(rows, currentDungeonStartMapRidableEntry{
				PositionX: binary.LittleEndian.Uint32(body[offset:]), PositionY: binary.LittleEndian.Uint32(body[offset+4:]),
				ObjectCode: binary.LittleEndian.Uint32(body[offset+8:]), Faction: binary.LittleEndian.Uint32(body[offset+12:]), State: binary.LittleEndian.Uint32(body[offset+16:]),
			})
			offset += 20
		}
	}
	if offset+1 != len(body) || body[offset] != 0xff {
		t.Fatalf("unexpected start-map tail: offset=%d body=%x", offset, body)
	}
	return rows
}

type ordinaryRidableMazeFixture struct {
	maze      int
	room      worldmap.RoomCoordinate
	positions []worldmap.Point
}

func ordinaryRidableEntryRuntime(
	t *testing.T,
	svc *Service,
	dungeon worldmap.Dungeon,
	topology *worldmap.DungeonTopology,
	coordinate worldmap.RoomCoordinate,
	owner *currentDungeonRandomizedOwner,
) (*runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	if svc == nil || svc.dungeonMonsterTable == nil {
		t.Fatal("ordinary ridable fixture needs the actual monster catalog")
	}
	if len(topology.Bosses) == 0 {
		t.Fatal("ordinary ridable fixture has no boss coordinate")
	}
	local := *topology
	start := coordinate
	local.Start = &start
	run, err := worldmap.NewDungeonRun(&local)
	if err != nil {
		t.Fatal(err)
	}
	session, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	scene, ok := session.Scene()
	if !ok || scene.Coordinate != coordinate {
		t.Fatalf("entry scene=%s want=%s", scene.Coordinate, coordinate)
	}
	room, next, err := newRuntimeDungeonRoom(scene, svc.dungeonMonsterTable, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	var ai *pvfDungeonAICharacterCatalog
	if len(scene.AICharacters) > 0 {
		ai, err = newPVFDungeonAICharacterCatalog(svc.initialEquipmentArchive)
		if err != nil {
			t.Fatal(err)
		}
	}
	actors, err := planRuntimeDungeonExtendedActors(scene, svc.dungeonMonsterTable, ai, dungeon.Metadata.BasisLevel, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := room.AttachExtendedActors(actors); err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeDungeonState{
		Request:           dungeoncmd.SelectDungeonRequest{DungeonID: uint32(dungeon.ID)},
		Dungeon:           dungeon,
		MazeIndex:         topology.MazeIndex,
		Session:           session,
		Room:              room,
		BossCoordinate:    topology.Bosses[0],
		BossSet:           true,
		Seed:              17834,
		randomizedObjects: owner,
	}
	return runtime, scene
}

func TestRealPVFOrdinaryRidableBreeding164EntryStartMap(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the actual a56 Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
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
	dungeon, ok := table.FindDungeon(164)
	if !ok {
		t.Fatal("actual PVF has no ordinary dungeon 164")
	}
	if !strings.EqualFold(dungeon.Path, "dungeon/stormofmetastasis/behemoth/breeding.dgn") {
		t.Fatalf("dungeon 164 path=%q", dungeon.Path)
	}
	svc := &Service{
		initialEquipmentArchive: archive,
		dungeonMonsterTable:     monsters,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
	}
	fixtures := []ordinaryRidableMazeFixture{
		{maze: 0, room: worldmap.RoomCoordinate{X: 2, Y: 1}, positions: []worldmap.Point{{X: 1003, Y: 248}, {X: 1154, Y: 207}, {X: 1298, Y: 252}, {X: 1196, Y: 297}}},
		{maze: 1, room: worldmap.RoomCoordinate{X: 3, Y: 1}, positions: []worldmap.Point{{X: 511, Y: 259}, {X: 369, Y: 187}, {X: 368, Y: 308}, {X: 275, Y: 258}}},
		{maze: 6, room: worldmap.RoomCoordinate{X: 2, Y: 1}, positions: []worldmap.Point{{X: 1003, Y: 248}, {X: 1154, Y: 207}, {X: 1298, Y: 252}, {X: 1196, Y: 297}}},
		{maze: 7, room: worldmap.RoomCoordinate{X: 2, Y: 1}, positions: []worldmap.Point{{X: 1003, Y: 248}, {X: 1154, Y: 207}, {X: 1298, Y: 252}, {X: 1196, Y: 297}}},
	}
	wantIDs := []int64{69000, 69001, 69002, 69003}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fmt.Sprintf("maze%d", fixture.maze), func(t *testing.T) {
			topology, err := worldmap.BuildDungeonLayout(resolver, 164, fixture.maze, func(choice worldmap.DungeonMapChoice) (int64, error) {
				if len(choice.Candidates) == 0 {
					return 0, fmt.Errorf("maze %d room %s has no map candidate", fixture.maze, choice.Coordinate)
				}
				return choice.Candidates[0].ID, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			scripts := dungeon.Mazes[fixture.maze].RandomizedObjects
			if len(scripts) != 1 {
				t.Fatalf("maze %d randomized groups=%d", fixture.maze, len(scripts))
			}
			script := scripts[0]
			if !script.SelectCount.Set || script.SelectCount.Value != 4 {
				t.Fatalf("maze %d select=%+v", fixture.maze, script.SelectCount)
			}
			if !script.Regenerate.Set || script.Regenerate.Value != 0 {
				t.Fatalf("maze %d regenerate=%+v", fixture.maze, script.Regenerate)
			}
			if len(script.Objects) != len(wantIDs) {
				t.Fatalf("maze %d candidates=%d", fixture.maze, len(script.Objects))
			}

			svc.dungeonChoice = func(int) (int, error) { return 0, nil }
			owner, err := svc.prepareCurrentDungeonRandomizedOwner(dungeon, fixture.maze, topology, nil)
			if err != nil {
				t.Fatalf("prepare current owner: %v", err)
			}
			if owner == nil || owner.Plan == nil {
				t.Fatal("current owner is missing")
			}
			if len(owner.Entries) != len(wantIDs) {
				t.Fatalf("selected entries=%d", len(owner.Entries))
			}
			for index, entry := range owner.Entries {
				if entry.Source.Group != 0 || entry.Source.SourceIndex != index {
					t.Fatalf("entry %d source group/index=%d/%d", index, entry.Source.Group, entry.Source.SourceIndex)
				}
				if entry.Source.ObjectID != wantIDs[index] {
					t.Fatalf("entry %d code=%d want=%d", index, entry.Source.ObjectID, wantIDs[index])
				}
				if entry.Source.Map != fixture.room {
					t.Fatalf("entry %d room=%s want=%s", index, entry.Source.Map, fixture.room)
				}
				if entry.Source.Position != fixture.positions[index] {
					t.Fatalf("entry %d position=%+v want=%+v", index, entry.Source.Position, fixture.positions[index])
				}
				if entry.Source.Team != "neutral" || entry.Source.ObjectType != "" {
					t.Fatalf("entry %d source type/team=%q/%q", index, entry.Source.ObjectType, entry.Source.Team)
				}
				if entry.Source.Regenerate != (worldmap.OptionalInt{Set: true, Value: 0}) {
					t.Fatalf("entry %d regenerate=%+v", index, entry.Source.Regenerate)
				}
				if entry.Faction != 0 || entry.SpawnMode != 0 {
					t.Fatalf("entry %d namespace/monster wire=%d/%d", index, entry.Faction, entry.SpawnMode)
				}
			}
			if err := owner.validate(dungeon, fixture.maze); err != nil {
				t.Fatal(err)
			}

			svc.dungeonChoice = func(int) (int, error) {
				t.Fatal("frozen follower rerolled the ordinary randomized selection")
				return 0, nil
			}
			follower, err := svc.prepareCurrentDungeonRandomizedOwner(dungeon, fixture.maze, topology, owner)
			if err != nil {
				t.Fatalf("frozen follower: %v", err)
			}
			if !reflect.DeepEqual(owner.Entries, follower.Entries) {
				t.Fatalf("frozen follower changed entries: owner=%+v follower=%+v", owner.Entries, follower.Entries)
			}
			if follower.Plan == owner.Plan {
				t.Fatal("frozen follower aliases the leader plan")
			}
			follower.Entries[0].Source.Position.X++
			if owner.Entries[0].Source.Position.X == follower.Entries[0].Source.Position.X {
				t.Fatal("frozen follower entry aliases the leader entry")
			}
			if _, err := svc.prepareCurrentDungeonRandomizedOwner(dungeon, fixture.maze, topology, follower); err == nil {
				t.Fatal("mutated frozen follower was accepted")
			}

			if topology.Start == nil {
				t.Fatal("actual topology start is missing")
			}
			entryStart := *topology.Start
			nonOwnerRoom := entryStart
			if nonOwnerRoom == fixture.room {
				for _, room := range topology.Rooms() {
					if room.Coordinate != fixture.room {
						nonOwnerRoom = room.Coordinate
						break
					}
				}
			}
			if nonOwnerRoom == fixture.room {
				t.Fatal("fixture has no non-owner room")
			}
			nonOwnerRuntime, nonOwnerScene := ordinaryRidableEntryRuntime(t, svc, dungeon, topology, nonOwnerRoom, owner)
			nonOwnerPackets, err := buildCurrentDungeonEntryPackets(nonOwnerRuntime, nonOwnerScene)
			if err != nil {
				t.Fatalf("non-owner entry packets: %v", err)
			}
			if rows := ordinaryRidableRows(t, nonOwnerPackets.StartMap); len(rows) != 0 {
				t.Fatalf("ordinary objects leaked into non-owner room %s: %+v", nonOwnerRoom, rows)
			}

			ownerRuntime, ownerScene := ordinaryRidableEntryRuntime(t, svc, dungeon, topology, fixture.room, owner)
			ownerPackets, err := buildCurrentDungeonEntryPackets(ownerRuntime, ownerScene)
			if err != nil {
				t.Fatalf("owner-room entry packets: %v", err)
			}
			if ownerPackets.StartMap[0] != byte(fixture.room.X) || ownerPackets.StartMap[1] != byte(fixture.room.Y) {
				t.Fatalf("owner-room packet coordinate=%d/%d want=%s", ownerPackets.StartMap[0], ownerPackets.StartMap[1], fixture.room)
			}
			rows := ordinaryRidableRows(t, ownerPackets.StartMap)
			wantRows := make([]currentDungeonStartMapRidableEntry, 0, len(fixture.positions))
			for index, position := range fixture.positions {
				wantRows = append(wantRows, currentDungeonStartMapRidableEntry{
					PositionX:  uint32(position.X),
					PositionY:  uint32(position.Y),
					ObjectCode: uint32(wantIDs[index]),
					Faction:    0,
					State:      0,
				})
			}
			if !reflect.DeepEqual(rows, wantRows) {
				t.Fatalf("owner-room START_MAP rows=%+v want=%+v", rows, wantRows)
			}
			t.Logf("maze=%d entry_start_nonowner=%s room=%s rows=%+v", fixture.maze, nonOwnerRoom, fixture.room, rows)
		})
	}
}
