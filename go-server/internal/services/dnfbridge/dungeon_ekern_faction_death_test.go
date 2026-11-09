package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestEkernFactionDeathRepairsFailedNotifyWithoutResettling(t *testing.T) {
	s, p, _ := ekernDiagnosticFixture(t, false)
	rt := p.dungeon.runtime
	rt.Dungeon.ID = 5097
	rt.Dungeon.Path = "dungeon/legend/ekern/warriors/warriors.dgn"
	monsters := rt.Room.Snapshot().Monsters
	target, contributor := monsters[1].ObjectKey, monsters[0].ObjectKey
	body := ekernDiagnosticBody(target, ^uint16(0), contributor)
	conn := p.conn.(*bufferConn)
	wantErr := errors.New("injected op38 write failure")
	p.conn = &failNthDungeonWriteConn{failAt: 1, err: wantErr}
	if err := s.handleDungeonMonsterDeath(p, body); !errors.Is(err, wantErr) {
		t.Fatalf("write error=%v", err)
	}
	if _, defeated := rt.Room.DefeatedDeathTarget(target); !defeated {
		t.Fatal("authoritative defeat missing after write failure")
	}
	p.conn = conn
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != target {
		t.Fatalf("retry did not repair death notification: %x", ack)
	}
	conn.write.Reset()
	if err := s.handleDungeonMonsterDeath(p, body); err != nil || conn.write.Len() != 0 {
		t.Fatalf("successful retry was repeated: %v %x", err, conn.write.Bytes())
	}
}

func TestEkernFactionDeathCommitsOnlyTargetAndAllowsClear(t *testing.T) {
	s, p, logs := ekernDiagnosticFixture(t, true)
	rt := p.dungeon.runtime
	rt.Dungeon.ID = 5097
	rt.Dungeon.Path = "dungeon/Legend/Ekern/Warriors/Warriors.dgn"
	monsters := rt.Room.Snapshot().Monsters
	target, contributor := monsters[1].ObjectKey, monsters[0].ObjectKey
	body := ekernDiagnosticBody(target, ^uint16(0), contributor)
	conn := p.conn.(*bufferConn)
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	ack := abyssOp38Bodies(t, conn.write.Bytes())
	if len(ack) != 1 || len(ack[0]) < 2 || uint32(binary.LittleEndian.Uint16(ack[0])) != target {
		t.Fatalf("wrong death notifications: %x", ack)
	}
	if _, live := rt.Room.AnnouncedMonster(contributor); !live || rt.Session.Snapshot().Scene.Cleared {
		t.Fatal("faction death removed contributor or cleared early")
	}
	if events := ekernDiagnosticEvents(t, logs); len(events) != 1 || events[0]["after_target_defeated"] != true {
		t.Fatalf("death did not commit: %v", events)
	}
	conn.write.Reset()
	if err := s.handleDungeonMonsterDeath(p, body); err != nil || conn.write.Len() != 0 {
		t.Fatalf("duplicate faction death emitted output: %v %x", err, conn.write.Bytes())
	}
	if err := s.handleDungeonMonsterDeath(p, ekernDiagnosticBody(contributor, currentSceneActorObjectKey(99), 0)); err != nil {
		t.Fatal(err)
	}
	if !rt.Session.Snapshot().Scene.Cleared {
		t.Fatal("room still not cleared")
	}
	if _, err := s.planCurrentDungeonMove(rt, dungeoncmd.MoveMapRequest{NextX: 1}); err != nil {
		t.Fatalf("ordinary exit still blocked: %v", err)
	}
}

func TestEkernFactionDeathRejectsUnprovenContribution(t *testing.T) {
	for _, tc := range []string{"same_team", "missing_team", "wrong_dungeon", "wrong_path", "wrong_room", "unknown_actor", "self", "wrong_kind", "zero_value", "boss_room", "wrong_layout", "opaque_tail", "wrong_owner", "extra_entry"} {
		t.Run(tc, func(t *testing.T) {
			s, p, _ := ekernDiagnosticFixture(t, false)
			rt := p.dungeon.runtime
			rt.Dungeon.ID = 5097
			rt.Dungeon.Path = "dungeon/legend/ekern/warriors/warriors.dgn"
			scene, _ := rt.Session.Scene()
			monsters := rt.Room.Snapshot().Monsters
			req, err := s.decodeCurrentDungeonMonsterDeath(ekernDiagnosticBody(monsters[1].ObjectKey, ^uint16(0), monsters[0].ObjectKey))
			if err != nil || !s.currentEkernFactionDeath(rt, scene, req) {
				t.Fatalf("invalid positive fixture: %v", err)
			}
			switch tc {
			case "same_team":
				scene.Map.Map.MonsterTeam[1] = 100
			case "missing_team":
				scene.Map.Map.MonsterTeam = nil
			case "wrong_dungeon":
				rt.Dungeon.ID++
			case "wrong_path":
				rt.Dungeon.Path = "dungeon/ordinary/test.dgn"
			case "wrong_room":
				scene.Coordinate.X++
			case "unknown_actor":
				binary.LittleEndian.PutUint16(req.CombatEntries[2:], 65000)
			case "self":
				binary.LittleEndian.PutUint16(req.CombatEntries[2:], uint16(req.RuntimeObjectKey))
			case "wrong_kind":
				binary.LittleEndian.PutUint16(req.CombatEntries, 0x111)
			case "zero_value":
				binary.LittleEndian.PutUint64(req.CombatEntries[4:], 0)
			case "boss_room":
				scene.Boss = true
			case "wrong_layout":
				req.Layout = dungeoncmd.DieMonsterRequestLayoutFixed62
			case "opaque_tail":
				req.OpaqueTail = []byte{0}
			case "wrong_owner":
				req.OwnerObjectKey = 1
			case "extra_entry":
				req.CombatEntryCount++
				req.CombatEntries = append(req.CombatEntries, make([]byte, dungeoncmd.Official249DieMonsterEntrySize)...)
			}
			if s.currentEkernFactionDeath(rt, scene, req) {
				t.Fatal("unproven report admitted")
			}
		})
	}
}
