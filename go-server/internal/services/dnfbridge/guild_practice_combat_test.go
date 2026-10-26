package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func practiceTestOwner(t *testing.T, s *Service, p *gameSession, r *runtimeDungeonState) {
	t.Helper()
	id, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		t.Fatal("missing bound identity")
	}
	r.Dungeon.Metadata.IntegerValues = map[string]int64{"agit war dungeon": 1}
	r.Dungeon.Metadata.Flags = map[string]bool{"friendly agit dungeon": true}
	r.Request = dungeoncmd.SelectDungeonRequest{DungeonID: uint32(r.Dungeon.ID), Difficulty: 3, RuntimeState: 1, RuntimeToken: 0xffff}
	r.guildPractice = &currentGuildPracticeRun{GuildID: 1, DungeonID: uint32(r.Dungeon.ID), Generation: id.generation, identity: id,
		Placements: []guild.Placement{{ID: 70, ObjectID: 71021, Area: 0, X: 10, Y: 20}, {ID: 4, ObjectID: 71503, Area: 0}, {ID: 2, ObjectID: 71022, Area: 0, X: 30, Y: 40}, {ID: 99, ObjectID: 71023, Area: 0}}}
	scene, _ := r.Session.Scene()
	scene.Map.Map.SourceSections = []worldmap.RawSection{
		{Name: "agitwar area index", Tokens: []pvf.Token{{Kind: pvf.TokenInt, Int: 0}}},
		{Name: "agit obejcts xpos reverse", Tokens: []pvf.Token{{Kind: pvf.TokenInt, Int: 1330}}},
	}
	if err := prepareCurrentGuildPracticeRoomTargets(r.guildPractice, r.Room, scene, guild.PracticeMonsterCatalog{71021: 70641, 71022: 70642, 71023: 70643}); err != nil {
		t.Fatal(err)
	}
}

func TestGuildPracticeSharedDeathZeroKeyAndRewardIsolation(t *testing.T) {
	s, p, q, r, other := partyDungeonDeathFixture(t)
	practiceTestOwner(t, s, p, r)
	practiceTestOwner(t, s, q, other)
	repos, _ := s.repositoryProvider()
	beforeP, _, _ := repos.Character.Load(context.Background(), "99")
	beforeQ, _, _ := repos.Character.Load(context.Background(), "100")
	room := r.Room
	if room != other.Room {
		t.Fatal("party room not shared")
	}
	if got := room.guildPractice.targets[0]; got.PlacementID != 70 || got.MonsterID != 70641 || got.X != 1320 || got.Y != 20 {
		t.Fatalf("first frozen slot=%+v", got)
	}
	if room.guildPractice.targets[1].PlacementID != 2 || room.guildPractice.targets[2].PlacementID != 99 || len(room.guildPractice.targets) != 4 {
		t.Fatalf("native append order=%+v", room.guildPractice.targets)
	}
	static := room.Snapshot().Monsters[0].ObjectKey
	for _, key := range []uint32{0, 1, 2, 0, static, static} {
		p.conn.(*bufferConn).write.Reset()
		q.conn.(*bufferConn).write.Reset()
		if err := s.handleDungeonMonsterDeath(p, currentDungeonVariableOwnerSentinelDeathBody(key)); err != nil {
			t.Fatal(err)
		}
		for _, peer := range []*gameSession{p, q} {
			wire := peer.conn.(*bufferConn).write.Bytes()
			packet, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
			if packet.Header.MsgID != 38 || !bytes.Equal(packet.Body, buildCurrentGuildPracticeDeathBody(uint16(key))) {
				t.Fatalf("key %d ACK=%+v", key, packet)
			}
			for len(tail) > 0 {
				packet, tail = splitCurrentGameServerUpperPacketAuto(t, tail)
				if packet.Header.MsgID != 31 || len(packet.Body) != 0 {
					t.Fatalf("unexpected practice output %d", packet.Header.MsgID)
				}
			}
		}
	}
	for _, active := range []*runtimeDungeonState{r, other} {
		if !active.guildPractice.Finished || active.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
			t.Fatal("member not completed")
		}
		if active.settlementEntrySent || active.bossDieCheckAccepted || active.settlementCardRewardCommitted || active.ordinaryFinalRoomClearAccepted {
			t.Fatal("ordinary settlement changed")
		}
	}
	afterP, _, _ := repos.Character.Load(context.Background(), "99")
	afterQ, _, _ := repos.Character.Load(context.Background(), "100")
	if !reflect.DeepEqual(beforeP, afterP) || !reflect.DeepEqual(beforeQ, afterQ) {
		t.Fatal("practice changed character assets/progress")
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentGuildPracticeDefeatedLocalActors(p, r); err != nil {
		t.Fatal(err)
	}
	wire := p.conn.(*bufferConn).write.Bytes()
	for key := uint16(0); key < 3; key++ {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = tail
		if packet.Header.MsgID != 38 || binary.LittleEndian.Uint16(packet.Body) != key {
			t.Fatalf("revisit actor %d", key)
		}
	}
	if len(wire) != 0 {
		t.Fatal("extra replay")
	}
}

func TestGuildPracticeRejectsUnownedDeathWithoutOrdinaryFallback(t *testing.T) {
	for _, mode := range []string{"unregistered", "foreign_owner", "wrong_generation", "missing_owner", "not_announced", "wrong_room", "fixed_sentinel", "tail"} {
		t.Run(mode, func(t *testing.T) {
			s, p, _, r, _ := partyDungeonDeathFixture(t)
			practiceTestOwner(t, s, p, r)
			body := currentDungeonVariableOwnerSentinelDeathBody(0)
			switch mode {
			case "unregistered":
				binary.LittleEndian.PutUint32(body, 39)
			case "foreign_owner":
				binary.LittleEndian.PutUint16(body[4:], 77)
			case "wrong_generation":
				r.guildPractice.Generation++
			case "missing_owner":
				r.guildPractice = nil
			case "not_announced":
				delete(r.Room.announcedSessions, r.Session)
			case "wrong_room":
				r.Room.mapID++
			case "fixed_sentinel":
				body = tutorialScopeFixedDeathBody(0)
			case "tail":
				body = append(body, 1)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonMonsterDeath(p, body); err != nil {
				t.Fatal(err)
			}
			if p.conn.(*bufferConn).write.Len() != 0 || r.Room.guildPractice.targets[0].Defeated {
				t.Fatal("unowned report accepted")
			}
		})
	}
}

func TestGuildPracticeMapProjectionRejectsMalformedAndMissingAreaHasNoLocalActors(t *testing.T) {
	s, p, _, r, _ := partyDungeonDeathFixture(t)
	practiceTestOwner(t, s, p, r)
	scene, _ := r.Session.Scene()
	r.Room.guildPractice = nil
	if err := prepareCurrentGuildPracticeRoomTargets(r.guildPractice, r.Room, scene, guild.PracticeMonsterCatalog{71021: 70641}); err != nil {
		t.Fatal(err)
	}
	if len(r.Room.guildPractice.targets) != 1 {
		t.Fatal("absent area created defenders")
	}
	scene.Map.Map.SourceSections = []worldmap.RawSection{{Name: "agitwar area index", Tokens: []pvf.Token{{Kind: pvf.TokenInt, Int: 8}}}}
	if _, _, err := currentGuildPracticeMapProjection(scene.Map.Map); err == nil {
		t.Fatal("invalid area accepted")
	}
}
