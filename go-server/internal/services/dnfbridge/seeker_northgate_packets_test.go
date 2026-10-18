package dnfbridge

import (
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// This reads the existing full START_MAP layout independently of roomGroups.
// It intentionally checks the wire spawn mode, not a guessed regenerate field.
func northgateRidableRows(t *testing.T, body []byte) []currentDungeonStartMapRidableEntry {
	t.Helper()
	if len(body) < 23 || body[13] != currentDungeonStartMapPayloadBuild {
		t.Fatalf("expected full first-visit start map: %x", body)
	}
	offset := 19 + 21*int(body[18])
	if offset >= len(body) {
		t.Fatalf("truncated actors: %x", body)
	}
	offset += 1 + 21*int(body[offset]) // existing default extra-entry width
	offset++                           // hell-party fog
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

func assertNorthgateRidableRoomRoundTrip(t *testing.T, svc *Service, runtime *runtimeDungeonState, entryBody []byte, resolver *worldmap.Resolver) {
	t.Helper()
	if runtime.randomizedObjects == nil || len(runtime.randomizedObjects.Entries) != 1 {
		t.Fatal("northgate singleton owner absent")
	}
	entry := runtime.randomizedObjects.Entries[0]
	want := []currentDungeonStartMapRidableEntry{{PositionX: uint32(entry.Source.Position.X), PositionY: uint32(entry.Source.Position.Y), ObjectCode: 61235, Faction: 0, State: 0}}
	checkRows := func(coordinate worldmap.RoomCoordinate, body []byte) {
		t.Helper()
		rows := northgateRidableRows(t, body)
		if coordinate == entry.Source.Map {
			if !reflect.DeepEqual(rows, want) {
				t.Fatalf("wrong object namespace/position/spawn mode: %+v want=%+v", rows, want)
			}
		} else if len(rows) != 0 {
			t.Fatalf("object leaked to non-owner room %s: %+v", coordinate, rows)
		}
	}
	start := runtime.Session.Snapshot().Scene.Coordinate
	checkRows(start, entryBody)
	topology, err := worldmap.BuildDungeonLayout(resolver, runtime.Dungeon.ID, runtime.MazeIndex, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	// Find a real connected route without passing a final boss room.
	queue := [][]worldmap.RoomCoordinate{{start}}
	seen := map[worldmap.RoomCoordinate]bool{start: true}
	var route []worldmap.RoomCoordinate
	for len(queue) > 0 {
		candidate := queue[0]
		queue = queue[1:]
		current := candidate[len(candidate)-1]
		if current == entry.Source.Map {
			route = candidate
			break
		}
		room, _ := topology.Room(current)
		for _, neighbor := range room.Neighbors {
			next, ok := topology.Room(neighbor.Coordinate)
			if !ok || seen[neighbor.Coordinate] || next.Boss {
				continue
			}
			seen[neighbor.Coordinate] = true
			queue = append(queue, append(append([]worldmap.RoomCoordinate(nil), candidate...), neighbor.Coordinate))
		}
	}
	if len(route) == 0 {
		t.Fatal("no real non-boss route to ridable room")
	}
	svc.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "northgate-ridable-test", selectedCharacterID: 19, dungeon: dungeonSessionState{runtime: runtime}}
	// prepareDungeonRuntime preflights; the entry sender normally announces
	// this first room once. Subsequent handleDungeonMoveMap calls announce
	// their own rooms, and cached revisits retain those bindings.
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	clearRoom := func() {
		t.Helper()
		scene := runtime.Session.Snapshot().Scene
		for _, hostile := range scene.BlockingHostiles {
			if scene.Cleared {
				break
			}
			for key, reference := range scene.RuntimeObjects {
				if reference != hostile {
					continue
				}
				if _, _, err := runtime.Room.CommitActorDeathReport(key, runtime.Session); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !runtime.Session.Snapshot().Scene.Cleared {
			t.Fatal("real room was not cleared through owned actor deaths")
		}
		if err := runtime.cacheCurrentDungeonRoom(); err != nil {
			t.Fatal(err)
		}
	}
	move := func(target worldmap.RoomCoordinate) []byte {
		t.Helper()
		conn.write.Reset()
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0], body[1] = byte(target.X), byte(target.Y)
		if err := svc.handleDungeonMoveMap(session, body); err != nil {
			t.Fatal(err)
		}
		packets := splitAllFinishBridgePackets(t, conn.write.Bytes())
		for _, packet := range packets {
			if packet.Header.MsgID == currentDungeonStartNotification {
				if runtime.Session.Snapshot().Scene.Coordinate != target {
					t.Fatal("start-map sent without matching room transition")
				}
				return packet.Body
			}
		}
		t.Fatalf("move to %s emitted no start-map: %x", target, conn.write.Bytes())
		return nil
	}
	for _, coordinate := range route[1:] {
		clearRoom()
		checkRows(coordinate, move(coordinate))
	}
	clearRoom()
	ownedRoom, ownedSeed := runtime.Room, runtime.Seed
	room, _ := topology.Room(entry.Source.Map)
	var exit worldmap.RoomCoordinate
	exitFound := false
	for _, neighbor := range room.Neighbors {
		next, ok := topology.Room(neighbor.Coordinate)
		if ok && !next.Boss {
			exit, exitFound = neighbor.Coordinate, true
			break
		}
	}
	if !exitFound {
		t.Fatal("no non-boss exit for round trip")
	}
	move(exit)
	clearRoom()
	body := move(entry.Source.Map)
	if len(body) != 16 || body[13] != currentDungeonStartMapPayloadCached || body[14] != 0 || runtime.Room != ownedRoom || runtime.Seed != ownedSeed {
		t.Fatalf("revisit must preserve cache without another spawn row: %x", body)
	}
	if err := runtime.randomizedObjects.validate(runtime.Dungeon, runtime.MazeIndex); err != nil {
		t.Fatal(err)
	}
	t.Logf("maze%d actual route=%v; first spawn five-u32 row, return cached16/no new row", runtime.MazeIndex, route)
}
