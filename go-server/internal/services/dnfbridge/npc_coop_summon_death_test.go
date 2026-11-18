package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func coopNPCContributionBody(t *testing.T, target, owner, contributor uint32, contributionKind uint16) []byte {
	t.Helper()
	body := friendlyAPCDeathBody(t, target, contributor, false)
	binary.LittleEndian.PutUint16(body[4:], uint16(owner))
	binary.LittleEndian.PutUint16(body[31:], contributionKind)
	binary.LittleEndian.PutUint16(body[33:], uint16(contributor))
	return body
}

func TestCoopNPCDeathRequiresCurrentRoomSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        uint16
		contributor uint32
		retired     bool
		valid       bool
	}{
		{"local attack with live parent", 0x111, 0, false, true},
		{"local attack after parent retirement", 0x111, 0, true, true},
		{"unknown contributor", 0x211, 61002, false, false},
		{"wrong actor kind", 0x211, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, conn, target, npc := prepareFriendlyAPCDeathTest(t)
			p.dungeon.runtime.Room.extendedActors[0].AICharacter.Code = 2001
			if tc.retired {
				retireCoopNPCThroughNativeDeath(t, s, p, conn, npc)
			}
			source := tc.contributor
			if source == 0 {
				source = npc
			}
			body := coopNPCContributionBody(t, target, 61001, source, tc.kind)
			if err := s.handleDungeonMonsterDeath(p, body); err != nil {
				t.Fatal(err)
			}
			_, dead := p.dungeon.runtime.Room.DefeatedDeathTarget(target)
			if dead != tc.valid {
				t.Fatal("death", dead)
			}
			if tc.valid {
				if !seekerQST012HasOp38(splitAllGameServerUpperPackets(t, conn.write.Bytes()), target) {
					t.Fatal("missing death notification")
				}
			} else if conn.write.Len() != 0 {
				t.Fatal("unproven source received death acknowledgement")
			}
		})
	}
}

func TestCoopNPCLocalSpawnGetsOnlyDeathNotification(t *testing.T) {
	s, p, conn, target, npc := prepareFriendlyAPCDeathTest(t)
	r := p.dungeon.runtime
	r.Room.extendedActors[0].AICharacter.Code = 6514
	const localKey = 61003
	body := coopNPCContributionBody(t, localKey, npc, 61002, 0x211)
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	if !seekerQST012HasOp38(splitAllGameServerUpperPackets(t, conn.write.Bytes()), localKey) {
		t.Fatal("missing local death notification")
	}
	for _, owner := range []uint32{uint32(p.selectedCharacterID), uint32(^uint16(0))} {
		conn.write.Reset()
		if err := s.handleDungeonMonsterDeath(p, coopNPCContributionBody(t, localKey, owner, 61002, 0x211)); err != nil {
			t.Fatal(err)
		}
		if !seekerQST012HasOp38(splitAllGameServerUpperPackets(t, conn.write.Bytes()), localKey) {
			t.Fatal("local target with local attack did not retire", owner)
		}
	}
	if _, ok := r.Room.AnnouncedMonster(target); !ok {
		t.Fatal("changed authored hostile")
	}
	if _, ok := r.Room.DefeatedDeathTarget(localKey); ok {
		t.Fatal("created server kill for client-local actor")
	}
	conn.write.Reset()
	if err := s.handleDungeonMonsterDeath(p, coopNPCContributionBody(t, localKey, 61001, 61002, 0x211)); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("accepted an unrelated local actor")
	}
}

// Use real cooperative NPC definitions in an isolated ordinary room. Only
// retiring the two spawn kinds gains a new source of authority.
func TestRealPVFCoopNPCOwnedSummonContribution(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("current PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonAICharacterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   int64
		mode string
	}{
		{810, "ordinary"}, {811, "ordinary"}, {2001, "ordinary"}, {2007, "ordinary"},
		{2012, "ordinary"}, {2013, "ordinary"}, {2014, "ordinary"}, {6514, "ordinary"},
		{6516, "ordinary"}, {6517, "ordinary"}, {11529, "ordinary"},
		{2013, "tutorial"}, {11529, "raid"},
	} {
		id := tc.id
		t.Run(fmt.Sprintf("%d/%s", id, tc.mode), func(t *testing.T) {
			actor, found, err := catalog.Find(id)
			if err != nil || !found {
				t.Fatalf("current PVF cooperative NPC %d missing: %v", id, err)
			}
			source := bridgeDungeonPVF(false)
			if tc.mode == "tutorial" {
				source["dungeon/test.dgn"] += "[tutorial dungeon]\n1\n"
			}
			source["map/dungeon/test/start.map"] += fmt.Sprintf(
				"[ai character]\n%d 30 20 0 `[character]` `[normal]` 0 0\n",
				id,
			)
			source["map/dungeon/test/start.map"] += "[special passive object]\n777 30 20 0 1 `[monster]` 3001 20 0 0 0\n"
			rt := prepareSyntheticDungeonRuntimeForEntryTest(t, source, catalog)
			if tc.mode == "raid" {
				rt.raidEntry = &currentRaidDungeonEntry{}
			}
			if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{
				currentClientVersion:  "official-24.9",
				dungeonDeathRoomProof: dungeonDeathRoomProofRoomProven,
				gameUpperBodyCodec:    gameUpperBodyCodecPlain,
			}}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: rt}}
			room := rt.Room.Snapshot()
			target := room.Monsters[0].ObjectKey
			var npc, child uint32
			for _, actor := range room.ExtendedActors {
				switch actor.Kind {
				case runtimeDungeonActorAICharacter:
					npc = actor.ObjectKey
				case runtimeDungeonActorSpecialMonster:
					child = actor.ObjectKey
				}
			}
			if npc == 0 || child == 0 {
				t.Fatal("missing map actors")
			}
			// A positive NPC contribution can identify the local attack even
			// after the fallback budget for untracked client hits is exhausted.
			rt.storyNPCNotificationRoom = rt.Room
			rt.storyNPCNotificationCount = currentDungeonPuppetNotificationOnlyLimit
			for _, owner := range []uint32{61001, uint32(^uint16(0))} {
				const localChild = uint32(61004)
				if err := s.handleDungeonMonsterDeath(session, coopNPCContributionBody(t, localChild, owner, npc, 0x111)); err != nil {
					t.Fatal(err)
				}
				want, err := buildCurrentDungeonDeathNotificationBody(localChild, currentDungeonDeathResponseMonster)
				packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
				if err != nil || len(packets) != 1 || packets[0].Header.MsgID != 38 || !bytes.Equal(packets[0].Body, want) {
					t.Fatal("expected only the client-local spawn's zero-drop removal")
				}
				if _, settled := rt.Room.DefeatedDeathTarget(localChild); settled {
					t.Fatal("client-local spawn was settled as a server kill")
				}
				conn.write.Reset()
			}
			if err := s.handleDungeonMonsterDeath(session, coopNPCContributionBody(t, child, uint32(^uint16(0)), npc, 0x111)); err != nil {
				t.Fatal(err)
			}
			if !seekerQST012HasOp38(splitAllGameServerUpperPackets(t, conn.write.Bytes()), child) {
				t.Fatal("special spawn did not retire")
			}
			if _, alive := rt.Room.AnnouncedMonster(target); !alive {
				t.Fatal("map blocker was consumed")
			}
			if scene, ok := rt.Session.Scene(); !ok || scene.Cleared || (rt.DropOwner != nil && len(rt.DropOwner.byObjectKey) != 0) {
				t.Fatal("spawn retirement cleared the room or registered loot")
			}
			if tc.mode != "ordinary" {
				return
			}
			conn.write.Reset()
			const summon = uint32(61003)
			body := coopNPCContributionBody(t, target, summon, npc,
				currentDungeonPuppetContributionKindAICharacter)
			if err := s.handleDungeonMonsterDeath(session, body); err != nil {
				t.Fatal(err)
			}
			if _, defeated := rt.Room.DefeatedDeathTarget(target); !defeated {
				t.Fatalf("current PVF NPC %d owned summon did not defeat the announced target", id)
			}
			packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			if !seekerQST012HasOp38(packets, target) {
				t.Fatalf("current PVF NPC %d owned summon produced no op38 for target=%d", id, target)
			}
			t.Logf("NPC id=%d name=%q path=%q target=%d summon=%d", id, actor.Name, actor.Path, target, summon)
		})
	}
}

func TestCoopNPCSpawnRetirementRequiresFriendlyRoomActor(t *testing.T) {
	for _, faction := range []string{"[monster]", "[neutral]", ""} {
		t.Run(faction, func(t *testing.T) {
			s, p, conn, ordinary, npc := prepareFriendlyAPCDeathTest(t)
			room := p.dungeon.runtime.Room
			room.extendedActors[0].AICharacter.Faction = faction
			if err := s.handleDungeonMonsterDeath(p, coopNPCContributionBody(t, 61003, 61001, npc, 0x111)); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatal("unproven faction authorized a local death notification")
			}
			if _, ok := room.AnnouncedMonster(ordinary); !ok {
				t.Fatal("ordinary monster changed")
			}
		})
	}
}
