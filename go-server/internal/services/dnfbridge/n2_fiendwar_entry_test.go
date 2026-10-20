package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestN2FiendWarPVFEvidence(t *testing.T) {
	f, _ := actualCurrentFiendWar(t, 1)
	a := f.service.initialEquipmentArchive
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("N2_EVIDENCE_DIR")
	if out == "" {
		out = t.TempDir()
	}
	dump := func(path, name string) {
		raw, e := a.ReadText(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(out, name), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	m, _ := table.FindMap(306000110)
	dump(m.Path, "map-306000110.txt")
	for _, id := range []int64{4953, 4978} {
		d, ok := table.FindDungeon(id)
		if !ok {
			t.Fatal(id)
		}
		t.Logf("dungeon=%d path=%s mazes=%d", id, d.Path, len(d.Mazes))
		dump(d.Path, fmt.Sprintf("dungeon-%d.txt", id))
		for _, maze := range d.Mazes {
			for _, spec := range maze.MapSpecifications {
				for _, mid := range spec.MapIDs {
					m, ok := table.FindMap(mid)
					if !ok {
						t.Fatal(mid)
					}
					t.Logf("map=%d path=%s", mid, m.Path)
					dump(m.Path, fmt.Sprintf("map-%d.txt", mid))
				}
			}
		}
	}
}

func TestN2FiendWarFullEntry(t *testing.T) {
	for _, area := range []int{30, 31} {
		t.Run(fmt.Sprint(area), func(t *testing.T) {
			f, ps := actualCurrentFiendWar(t, 1)
			s, p := f.service, ps[0]
			ctx := context.Background()
			b := f.state.Battle
			f.state.Started = true
			b.EnterAfter = time.Now().Add(-time.Minute)
			b.Deadline = time.Now().Add(time.Hour)
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
			b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
			syncCurrentFiendWarDungeons(b)
			b.FiendWar.MemberAreas[p.selectedCharacterID] = area
			setCurrentFiendWarTestOrigin(p, area)
			table, err := worldmap.LoadSource(ctx, s.initialEquipmentArchive, worldmap.Options{})
			if err != nil {
				t.Fatal(err)
			}
			s.worldMapTable = table
			s.worldMapResolver, err = worldmap.NewResolverWithOptions(table, s.dungeonResolverOptions())
			if err != nil {
				t.Fatal(err)
			}
			s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(s.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			s.dungeonAICharacterTable, err = newPVFDungeonAICharacterCatalog(s.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			s.dungeonChoice = func(int) (int, error) { return 0, nil }
			s.dungeonSeed = func() (uint32, error) { return 1, nil }
			c, _, _ := f.repos.Character.Load(ctx, "19")
			c.Level = 90
			if c.Stats == nil {
				c.Stats = map[string]int64{}
			}
			c.Stats["fatigue"] = 156
			c.Stats["town_id"] = 98
			c.Stats["area_id"] = int64(area)
			if err = f.repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			id := b.FiendWar.Config.Areas[area].Dungeon
			body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id)})
			if err = s.handleDungeonSelectUpper(p, body); err != nil {
				t.Fatal(err)
			}
			r := p.dungeon.runtime
			if r == nil {
				t.Fatal("entry runtime absent", f.logText(t))
			}
			scene, ok := r.Session.Scene()
			if !ok {
				t.Fatal("scene missing")
			}
			t.Logf("dungeon=%d map=%d path=%s postStart=%v finish=%v", id, scene.Map.Map.ID, scene.Map.Map.Path, p.postStartMapPlayerStateSent, p.currentFinishLoadingStateSent)
			if err = s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
				t.Fatal(err)
			}
			t.Logf("after op37 postStart=%v finish=%v", p.postStartMapPlayerStateSent, p.currentFinishLoadingStateSent)
			data := p.conn.(*bufferConn).write.Bytes()
			var packets []map[string]any
			startPackets := 0
			for len(data) > 0 {
				packet, rest := splitGameServerUpperPacket(t, data)
				if packet.Header.MsgID == 29 && packet.Header.Classification == 0 {
					startPackets++
					if len(packet.Body) < 18 || int64(binary.LittleEndian.Uint32(packet.Body[14:18])) != scene.Map.Map.ID {
						t.Fatal("op29/runtime map mismatch")
					}
				}
				data = rest
				packets = append(packets, map[string]any{"op": packet.Header.MsgID, "class": packet.Header.Classification, "body": fmt.Sprintf("%x", packet.Body)})
			}
			if startPackets != 1 {
				t.Fatalf("op29 count=%d", startPackets)
			}
			out := os.Getenv("N2_EVIDENCE_DIR")
			if out == "" {
				out = t.TempDir()
			}
			enc, _ := json.MarshalIndent(packets, "", "  ")
			if err = os.WriteFile(filepath.Join(out, fmt.Sprintf("entry-%d.json", id)), enc, 0600); err != nil {
				t.Fatal(err)
			}
			want := int64(306000458)
			if id == 4978 {
				want = 306000461
			}
			if scene.Map.Map.ID != want {
				t.Errorf("PVF explicit start map mismatch: got=%d want=%d", scene.Map.Map.ID, want)
			}
			if len(scene.Map.Map.DungeonStartArea) != 4 {
				t.Fatal("missing PVF start area")
			}
			move := make([]byte, 7)
			x, y := uint16(scene.Map.Map.DungeonStartArea[0]), uint16(scene.Map.Map.DungeonStartArea[1])
			binary.LittleEndian.PutUint16(move, x)
			binary.LittleEndian.PutUint16(move[2:], y)
			if err = s.handleTownSetUserPosition(p, move); err != nil {
				t.Fatal(err)
			}
			if !r.actorPositionValid || r.actorPositionX != x || r.actorPositionY != y {
				t.Fatal("valid in-dungeon position report rejected")
			}
			if !p.postStartMapPlayerStateSent || !p.currentFinishLoadingStateSent {
				t.Fatal("scene ready missing")
			}
			if !scene.Cleared {
				t.Fatal("PVF start room should be empty; do not bypass room-clear rules")
			}
			// A nonadjacent room remains rejected. Then use the real right-hand
			// pathgate position to enter the declared boss room through op45.
			next := make([]byte, dungeoncmd.MoveMapRequestSize)
			next[0] = 9
			if err = s.handleDungeonMoveMap(p, next); err != nil {
				t.Fatal(err)
			}
			unchanged, _ := r.Session.Scene()
			if unchanged.Map.Map.ID != want {
				t.Fatal("nonadjacent movement changed the room")
			}
			next[0] = 1
			gate := scene.Map.Map.PathgatePositions[1]
			binary.LittleEndian.PutUint32(next[2:], uint32(gate.X))
			binary.LittleEndian.PutUint32(next[6:], uint32(gate.Y))
			p.conn.(*bufferConn).write.Reset()
			if err = s.handleDungeonMoveMap(p, next); err != nil {
				t.Fatal(err)
			}
			bossScene, _ := r.Session.Scene()
			if bossScene.Map.Map.ID != want+2 {
				t.Fatalf("op45 boss map got=%d want=%d; %s", bossScene.Map.Map.ID, want+2, f.logText(t))
			}
			t.Logf("op45 reached declared boss map=%d via PVF gate=%v", bossScene.Map.Map.ID, gate)
			if err = os.WriteFile(filepath.Join(out, fmt.Sprintf("move-%d.bin", id)), p.conn.(*bufferConn).write.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
