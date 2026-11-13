package dnfbridge

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFCoopNPCDefinitionAudit(t *testing.T) {
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
	ids := make([]int64, 0, len(catalog.paths))
	for id := range catalog.paths {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matched := 0
	for _, id := range ids {
		actor, found, err := catalog.Find(id)
		if err != nil || !found {
			continue
		}
		name := strings.ToLower(actor.Name + " " + actor.Path)
		if strings.Contains(name, "巴恩") || strings.Contains(name, "帕丽") || strings.Contains(name, "paris") || strings.Contains(name, "barn") || id == 2001 || id == 2012 {
			t.Logf("NPC id=%d name=%q path=%q", id, actor.Name, actor.Path)
			matched++
		}
	}
	t.Logf("catalog=%d matching_definitions=%d pvf=%s", len(ids), matched, archive.Snapshot().Checksum)
	if matched == 0 {
		t.Fatal("reported cooperative NPC definitions were not found")
	}
}

// The AIC definitions come from the exact current PVF; the small owned room
// is an isolated fixture, not a claim of complete main-quest playthrough.
func TestRealPVFCoopNPCDefinitionsRetirementAndLingeringLifecycle(t *testing.T) {
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
	for _, id := range []int64{2001, 2012, 6514, 6516, 6517} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			actor, found, err := catalog.Find(id)
			if err != nil || !found {
				t.Fatalf("current PVF NPC %d missing: %v", id, err)
			}
			if !strings.Contains(strings.ToLower(actor.Path), "bhan") && !strings.Contains(strings.ToLower(actor.Path), "paris") {
				t.Fatalf("unexpected NPC definition: %s", actor.Path)
			}
			source := bridgeDungeonPVF(false)
			source["map/dungeon/test/start.map"] += fmt.Sprintf("[ai character]\n%d 30 20 0 `[character]` `[normal]` 0 0\n", id)
			rt := prepareSyntheticDungeonRuntimeForEntryTest(t, source, catalog)
			if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: rt}}
			room := rt.Room.Snapshot()
			target, npc := room.Monsters[0].ObjectKey, room.ExtendedActors[0].ObjectKey
			retireCoopNPCThroughNativeDeath(t, s, session, conn, npc)
			if err := s.handleDungeonMonsterDeath(session, friendlyAPCDeathBody(t, target, npc, false)); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			scene, _ := rt.Session.Scene()
			if !scene.Cleared || !seekerQST012HasOp38(packets, target) {
				t.Fatalf("PVF NPC %d lingering contribution left an uncleared hostile", id)
			}
		})
	}
}
