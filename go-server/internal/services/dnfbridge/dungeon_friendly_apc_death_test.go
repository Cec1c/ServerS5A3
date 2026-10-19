package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func prepareFriendlyAPCDeathTest(t *testing.T) (*Service, *gameSession, *bufferConn, uint32, uint32) {
	t.Helper()
	return prepareFriendlyAPCDeathTestProfile(t, false)
}

func prepareFriendlyAPCDeathTestWithTutorial(t *testing.T) (*Service, *gameSession, *bufferConn, uint32, uint32) {
	t.Helper()
	return prepareFriendlyAPCDeathTestProfile(t, true)
}

func prepareFriendlyAPCDeathTestProfile(t *testing.T, tutorial bool) (*Service, *gameSession, *bufferConn, uint32, uint32) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	if tutorial {
		source["dungeon/test.dgn"] += "[tutorial dungeon]\n1\n"
	}
	source["map/dungeon/test/start.map"] += "[ai character]\n4002 30 20 0 `[character]` `[normal]` 0 0\n"
	source[defaultDungeonAICharacterList] = "4002 `Test/Friend.aic`\n"
	source["AICharacter/Test/Friend.aic"] = "[minimum info]\n`Friend APC` 1 2 3 4 25\n"
	catalog, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := prepareSyntheticDungeonRuntimeForEntryTest(t, source, catalog)
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	room := runtime.Room.Snapshot()
	return service, session, conn, room.Monsters[0].ObjectKey, room.ExtendedActors[0].ObjectKey
}

func TestOfficialFriendlyAPCDeathRewardsOnce(t *testing.T) {
	s, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
	s.options.currentClientVersion = "official-24.9"
	// Use the existing PVF/repository reward fixture and a previously announced
	// follower. Replays must preserve durable progression.
	actor := runtimeDungeonExtendedActor{Kind: runtimeDungeonActorAICharacter, ObjectKey: 1000, State: runtimeDungeonMonsterAnnounced}
	runtime.Rooms[runtimeDungeonRoomKey{X: 1, MapID: 101}] = &runtimeDungeonRoomVisit{Room: dungeonDeathOwnerTestRoom(nil, []runtimeDungeonExtendedActor{actor})}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	body := friendlyAPCDeathBody(t, runtime.Room.Snapshot().Monsters[0].ObjectKey, actor.ObjectKey, true)
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	character, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found || character.Stats["exp"] != 200 {
		t.Fatalf("experience not awarded: %+v %v", character, err)
	}
	skill, _, err := repositories.Skill.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		conn.write.Reset()
		if err := s.handleDungeonMonsterDeath(session, body); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 38 || len(rest) != 0 {
			t.Fatal("replay emitted additional reward packets")
		}
		after, _, err := repositories.Character.Load(context.Background(), "99")
		if err != nil || !reflect.DeepEqual(character, after) {
			t.Fatal("replay changed durable character")
		}
		afterSkill, _, err := repositories.Skill.Load(context.Background(), "99")
		if err != nil || !reflect.DeepEqual(skill, afterSkill) {
			t.Fatal("replay changed durable skill points")
		}
	}
}

// Native op39 samples: owner=ffff is independent of the nonempty contribution
// list. Rebind only object identifiers to this isolated runtime; retain the
// observed entry widths, values and tail instead of inventing another layout.
func friendlyAPCDeathBody(t *testing.T, target, apc uint32, mixed bool) []byte {
	t.Helper()
	sample := "92010000ffff00000000000000000000000066350000000000000b000000011101930166350000000000000b0084021901000000008e31001900000035260000000000001302190132021901090024000f00000003b245f2"
	if mixed {
		sample = "92010000ffff000000000000000000000000af3b000000000000040000000211019301533b00000000000003001101630019000000000000000100c2021a0100000000ef73001900000035260000000000000c031a010c031a01090024000c0000007833c4d9"
	}
	body, err := hex.DecodeString(sample)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(body, target)
	binary.LittleEndian.PutUint16(body[33:], uint16(apc))
	if mixed {
		binary.LittleEndian.PutUint16(body[47:], 99)
	}
	return body
}

func TestOfficialFriendlyAPCSentinelDeathAndReplay(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "apc-only"
		if mixed {
			name = "apc-and-player"
		}
		t.Run(name, func(t *testing.T) {
			s, session, conn, target, apc := prepareFriendlyAPCDeathTest(t)
			log := newRecordedPacketLog(t, s)
			t.Cleanup(func() { _ = s.packetLog.Close() })
			body := friendlyAPCDeathBody(t, target, apc, mixed)
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			rt := session.dungeon.runtime
			if rt.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated {
				t.Fatal("APC death was rejected")
			}
			scene, _ := rt.Session.Scene()
			if !scene.Cleared {
				t.Fatal("last hostile did not clear room")
			}
			if !rt.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(apc) {
				t.Fatal("killer APC was retired")
			}
			packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(target) {
				t.Fatalf("missing op38: %+v", packet)
			}
			first := append([]byte(nil), packet.Body...)
			conn.write.Reset()
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if !bytes.Equal(first, packet.Body) || len(rest) != 0 {
				t.Fatal("replay changed death or emitted rewards")
			}
			if bytes.Count([]byte(log()), []byte("kind=game-dungeon-monster-death-accepted ")) != 1 {
				t.Fatal("death settled more than once")
			}
		})
	}
}

func TestOfficialFriendlyAPCDeathInPVFTutorial(t *testing.T) {
	tests := []struct {
		name string
		body func(*testing.T, uint32, uint32) []byte
	}{
		{
			name: "apc-only",
			body: func(t *testing.T, target, apc uint32) []byte {
				return friendlyAPCDeathBody(t, target, apc, false)
			},
		},
		{
			name: "apc-and-player",
			body: func(t *testing.T, target, apc uint32) []byte {
				return friendlyAPCDeathBody(t, target, apc, true)
			},
		},
		{
			name: "player-last-kill",
			body: func(t *testing.T, target, apc uint32) []byte {
				body := friendlyAPCDeathBody(t, target, apc, true)
				binary.LittleEndian.PutUint16(body[4:], 99)
				return body
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, session, conn, target, apc := prepareFriendlyAPCDeathTestWithTutorial(t)
			runtime := session.dungeon.runtime
			if !isPVFTutorialDungeon(runtime) {
				t.Fatal("fixture did not carry the PVF [tutorial dungeon] metadata")
			}
			if err := s.handleDungeonMonsterDeath(session, test.body(t, target, apc)); err != nil {
				t.Fatal(err)
			}
			if state := runtime.Room.Snapshot().Monsters[0].State; state != runtimeDungeonMonsterDefeated {
				t.Fatalf("tutorial death state=%s want defeated", state)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != uint16(target) || len(rest) != 0 {
				t.Fatalf("missing tutorial op38: header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
			}
			scene, _ := runtime.Session.Scene()
			if !scene.Cleared {
				t.Fatal("last tutorial hostile did not clear the room")
			}
			if !runtime.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(apc) {
				t.Fatal("tutorial killer APC was retired")
			}
		})
	}
}

func TestOfficialFriendlyAPCDeathInPVFTutorialReplay(t *testing.T) {
	s, session, conn, target, apc := prepareFriendlyAPCDeathTestWithTutorial(t)
	log := newRecordedPacketLog(t, s)
	t.Cleanup(func() { _ = s.packetLog.Close() })
	body := friendlyAPCDeathBody(t, target, apc, false)
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	first := append([]byte(nil), packet.Body...)
	conn.write.Reset()
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 38 || !bytes.Equal(first, packet.Body) || len(rest) != 0 {
		t.Fatal("tutorial replay changed the authoritative death notification")
	}
	if bytes.Count([]byte(log()), []byte("kind=game-dungeon-monster-death-accepted ")) != 1 {
		t.Fatal("tutorial death settled more than once")
	}
}

func TestOfficialFriendlyAPCDeathInPVFTutorialRejectsUnproven(t *testing.T) {
	for _, name := range []string{
		"unknown-contributor",
		"player-only-entry",
		"wrong-kind",
		"hostile-apc",
		"planned-apc",
		"retired-apc",
		"unknown-target",
		"apc-target",
		"planned-target",
		"room-mismatch",
		"cross-room-apc",
		"stale-room-target",
		"tutorial-namespace-without-flag",
	} {
		t.Run(name, func(t *testing.T) {
			tutorial := name != "tutorial-namespace-without-flag"
			s, session, conn, target, apc := prepareFriendlyAPCDeathTestProfile(t, tutorial)
			runtime := session.dungeon.runtime
			body := friendlyAPCDeathBody(t, target, apc, false)
			switch name {
			case "unknown-contributor":
				binary.LittleEndian.PutUint16(body[33:], 60000)
			case "player-only-entry":
				binary.LittleEndian.PutUint16(body[33:], 99)
			case "wrong-kind":
				binary.LittleEndian.PutUint16(body[31:], 0x110)
			case "hostile-apc":
				runtime.Room.extendedActors[0].HostileReference = &worldmap.HostileReference{Kind: worldmap.HostileAICharacter}
			case "planned-apc":
				runtime.Room.extendedActors[0].State = runtimeDungeonMonsterPlanned
			case "retired-apc":
				runtime.Room.extendedActors[0].State = runtimeDungeonMonsterDefeated
			case "unknown-target":
				binary.LittleEndian.PutUint32(body, 60000)
			case "apc-target":
				binary.LittleEndian.PutUint32(body, apc)
			case "planned-target":
				runtime.Room.monsters[0].State = runtimeDungeonMonsterPlanned
			case "room-mismatch":
				runtime.Room.mapID++
			case "cross-room-apc":
				actor := runtime.Room.extendedActors[0]
				runtime.Room.extendedActors = nil
				runtime.Room.extendedByObjectKey = map[uint32]int{}
				old := dungeonDeathOwnerTestRoom(nil, []runtimeDungeonExtendedActor{actor})
				runtime.Rooms[runtimeDungeonRoomKey{X: 1, Y: 0, MapID: 101}] = &runtimeDungeonRoomVisit{Room: old}
			case "stale-room-target":
				old := dungeonDeathOwnerTestRoom([]runtimeDungeonMonster{{ObjectKey: 1000, State: runtimeDungeonMonsterAnnounced}}, nil)
				runtime.Rooms[runtimeDungeonRoomKey{X: 1, Y: 0, MapID: 101}] = &runtimeDungeonRoomVisit{Room: old}
				binary.LittleEndian.PutUint32(body, 1000)
			case "tutorial-namespace-without-flag":
				runtime.Dungeon.Path = "dungeon/tutorial/test.dgn"
			}
			before := runtime.Room.Snapshot().Monsters[0].State
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if runtime.Room.Snapshot().Monsters[0].State != before || conn.write.Len() != 0 {
				t.Fatal("unproven tutorial death changed state or sent an acknowledgement")
			}
		})
	}
}

func TestOfficialFriendlyAPCDeathRejectsUnprovenRequests(t *testing.T) {
	for _, name := range []string{"unknown-contributor", "player-only", "wrong-kind", "zero-contribution", "hostile-apc", "planned-apc", "retired-apc", "unknown-target", "apc-target", "planned-target", "room-mismatch", "tutorial", "extension", "truncated", "legacy-profile", "unknown-owner"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn, target, apc := prepareFriendlyAPCDeathTest(t)
			rt := session.dungeon.runtime
			body := friendlyAPCDeathBody(t, target, apc, false)
			switch name {
			case "unknown-contributor":
				binary.LittleEndian.PutUint16(body[33:], 60000)
			case "player-only":
				binary.LittleEndian.PutUint16(body[33:], 99)
			case "wrong-kind":
				binary.LittleEndian.PutUint16(body[31:], 0x110)
			case "zero-contribution":
				binary.LittleEndian.PutUint64(body[35:], 0)
			case "hostile-apc":
				rt.Room.extendedActors[0].HostileReference = &worldmap.HostileReference{Kind: worldmap.HostileAICharacter}
			case "planned-apc":
				rt.Room.extendedActors[0].State = runtimeDungeonMonsterPlanned
			case "retired-apc":
				rt.Room.extendedActors[0].State = runtimeDungeonMonsterDefeated
			case "unknown-target":
				binary.LittleEndian.PutUint32(body, 60000)
			case "apc-target":
				binary.LittleEndian.PutUint32(body, apc)
			case "planned-target":
				rt.Room.monsters[0].State = runtimeDungeonMonsterPlanned
			case "room-mismatch":
				rt.Room.mapID++
			case "tutorial":
				rt.Dungeon.Path = "dungeon/tutorial/test.dgn"
			case "extension":
				body = append(body, 0)
			case "truncated":
				body = body[:len(body)-1]
			case "legacy-profile":
				s.options.currentClientVersion = "90cn-decode-bypass-v1"
			case "unknown-owner":
				binary.LittleEndian.PutUint16(body[4:], 60000)
			}
			before := rt.Room.Snapshot().Monsters[0].State
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if rt.Room.Snapshot().Monsters[0].State != before || conn.write.Len() != 0 {
				t.Fatal("unproven death changed state or sent acknowledgement")
			}
		})
	}
}

func TestOfficialFriendlyAPCDeathWriteFailureRetry(t *testing.T) {
	s, session, conn, target, apc := prepareFriendlyAPCDeathTest(t)
	log := newRecordedPacketLog(t, s)
	t.Cleanup(func() { _ = s.packetLog.Close() })
	wantErr := errors.New("injected op38 write failure")
	session.conn = &failNthDungeonWriteConn{failAt: 1, err: wantErr}
	body := friendlyAPCDeathBody(t, target, apc, false)
	if err := s.handleDungeonMonsterDeath(session, body); !errors.Is(err, wantErr) {
		t.Fatalf("write error=%v", err)
	}
	if session.dungeon.runtime.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated {
		t.Fatal("authoritative death missing")
	}
	session.conn = conn
	if err := s.handleDungeonMonsterDeath(session, body); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != 38 || len(rest) != 0 {
		t.Fatal("retry did not repair death notification")
	}
	if bytes.Count([]byte(log()), []byte("kind=game-dungeon-monster-drop-deferred ")) > 1 {
		t.Fatal("retry planned rewards again")
	}
}

func TestOfficialFriendlyAPCFollowerAndCrossRoomTarget(t *testing.T) {
	for _, name := range []string{"follower", "retired-follower", "cross-room-target"} {
		t.Run(name, func(t *testing.T) {
			s, session, conn, target, apc := prepareFriendlyAPCDeathTest(t)
			rt := session.dungeon.runtime
			actor := rt.Room.extendedActors[0]
			rt.Room.extendedActors = nil
			rt.Room.extendedByObjectKey = map[uint32]int{}
			if name == "retired-follower" {
				actor.State = runtimeDungeonMonsterDefeated
			}
			old := dungeonDeathOwnerTestRoom([]runtimeDungeonMonster{{ObjectKey: 1000, State: runtimeDungeonMonsterAnnounced}}, []runtimeDungeonExtendedActor{actor})
			rt.Rooms[runtimeDungeonRoomKey{X: 1, Y: 0, MapID: 101}] = &runtimeDungeonRoomVisit{Room: old}
			if name == "cross-room-target" {
				target = 1000
			}
			if err := s.handleDungeonMonsterDeath(session, friendlyAPCDeathBody(t, target, apc, false)); err != nil {
				t.Fatal(err)
			}
			if name == "follower" {
				if rt.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated || conn.write.Len() == 0 {
					t.Fatal("follower kill was rejected")
				}
			} else if rt.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterAnnounced || old.monsters[0].State != runtimeDungeonMonsterAnnounced || conn.write.Len() != 0 {
				t.Fatal("stale follower or cross-room target was accepted")
			}
		})
	}
}
