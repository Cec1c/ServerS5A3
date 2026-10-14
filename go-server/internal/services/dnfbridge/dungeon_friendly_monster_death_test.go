package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func prepareFriendlyMonsterDeathTest(t *testing.T) (*Service, *gameSession, *bufferConn, uint32, uint32) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["map/dungeon/test/start.map"] += "3001 10 0 200 200 0 0 0 `[fixed]` `[normal]`\n[monster team]\n100 0\n"
	runtime := prepareSyntheticDungeonRuntimeForEntryTest(t, source, nil)
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	monsters := runtime.Room.Snapshot().Monsters
	return s, session, conn, monsters[0].ObjectKey, monsters[1].ObjectKey
}

// Preserve the native monster-kind contribution layout, rebinding object IDs
// only. No player identity, transport header or connection data is retained.
func friendlyMonsterDeathBody(t *testing.T, target, ally uint32) []byte {
	t.Helper()
	body, err := hex.DecodeString("a6010000ffff0000000000000000000000008161000000000000020000000111029401466100000000000002005c03c5000000000066750021000000ca5e0000000000000103cf003e03c5000d0015002a0000006356b1b7")
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(body, target)
	binary.LittleEndian.PutUint16(body[33:], uint16(ally))
	return body
}

func TestOfficialFriendlyMonsterDeathProof(t *testing.T) {
	for _, name := range []string{"current-room", "follower", "hostile-team", "missing-team", "planned", "retired", "wrong-kind", "unknown-kind", "unknown-key", "zero-contribution", "cross-room-target", "friendly-target", "wrong-room-metadata", "wrong-monster-metadata", "unbound-contributor", "unrelated-kind-same-key"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn, target, ally := prepareFriendlyMonsterDeathTest(t)
			rt := session.dungeon.runtime
			scene, _ := rt.Session.Scene()
			body := friendlyMonsterDeathBody(t, target, ally)
			// Cache an originating room for every case so the checks also cover
			// a follower whose original key survives a room transition.
			old := dungeonDeathOwnerTestRoom([]runtimeDungeonMonster{rt.Room.monsters[1]}, nil)
			old.coordinate = worldmap.RoomCoordinate{X: 1}
			old.mapID = 101
			oldScene := scene
			oldScene.Coordinate, oldScene.Map.Map.ID = old.coordinate, old.mapID
			rt.Rooms = map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit{
				{X: 1, MapID: 101}: {Room: old, Scene: oldScene},
			}
			if name != "current-room" && name != "friendly-target" {
				rt.Room.monsters = rt.Room.monsters[:1]
				delete(rt.Room.byObjectKey, ally)
			}
			switch name {
			case "hostile-team":
				oldScene.Map.Map.MonsterTeam[1] = 100
			case "missing-team":
				oldScene.Map.Map.MonsterTeam = nil
			case "planned":
				old.monsters[0].State = runtimeDungeonMonsterPlanned
			case "retired":
				old.monsters[0].State = runtimeDungeonMonsterDefeated
			case "wrong-kind":
				binary.LittleEndian.PutUint16(body[31:], 0x111)
			case "unknown-kind":
				binary.LittleEndian.PutUint16(body[31:], 0x311)
			case "unknown-key":
				binary.LittleEndian.PutUint16(body[33:], 60000)
			case "zero-contribution":
				binary.LittleEndian.PutUint64(body[35:], 0)
			case "cross-room-target":
				binary.LittleEndian.PutUint32(body, ally)
			case "friendly-target":
				binary.LittleEndian.PutUint32(body, ally)
			case "wrong-room-metadata":
				oldScene.Map.Map.ID++
			case "wrong-monster-metadata":
				oldScene.Map.Map.Monsters[1].MonsterID++
			case "unbound-contributor":
				delete(oldScene.RuntimeObjects, ally)
			case "unrelated-kind-same-key":
				old.monsters = nil
				old.byObjectKey = nil
			}
			rt.Rooms[runtimeDungeonRoomKey{X: 1, MapID: 101}].Scene = oldScene
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			_, defeated := rt.Room.DefeatedDeathTarget(target)
			accepted := name == "current-room" || name == "follower"
			if defeated != accepted {
				t.Fatalf("defeated=%t want=%t", defeated, accepted)
			}
			if !accepted {
				if conn.write.Len() != 0 {
					t.Fatal("unproven contribution produced a reply")
				}
				return
			}
			if _, ok := old.AnnouncedMonster(ally); !ok {
				t.Fatal("friendly contributor was retired")
			}
			after, _ := rt.Session.Scene()
			if !after.Cleared {
				t.Fatal("last hostile left door blocked")
			}
			packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(target) {
				t.Fatal("missing death reply")
			}
			first := append([]byte(nil), packet.Body...)
			conn.write.Reset()
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(first, packet.Body) || len(rest) != 0 {
				t.Fatal("replay did more than replay the death reply")
			}
		})
	}
}

func TestOfficialFriendlyMonsterDeathWriteFailureRetry(t *testing.T) {
	s, session, conn, target, ally := prepareFriendlyMonsterDeathTest(t)
	log := newRecordedPacketLog(t, s)
	t.Cleanup(func() { _ = s.packetLog.Close() })
	want := errors.New("injected death notification failure")
	session.conn = &failNthDungeonWriteConn{failAt: 1, err: want}
	body := friendlyMonsterDeathBody(t, target, ally)
	if err := s.handleDungeonMonsterDeath(session, body); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if _, ok := session.dungeon.runtime.Room.DefeatedDeathTarget(target); !ok {
		t.Fatal("authoritative death not committed")
	}
	session.conn = conn
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 38 || len(rest) != 0 {
		t.Fatal("retry settled rewards again")
	}
	if bytes.Count([]byte(log()), []byte("kind=game-dungeon-monster-drop-deferred ")) > 1 {
		t.Fatal("duplicate drop settlement")
	}
}
