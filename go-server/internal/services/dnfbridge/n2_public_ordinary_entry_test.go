package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"testing"
)

// TestRealPVFN2PublicOrdinaryEntry is intentionally independent of !158.
// Dedicated FiendWar channel admission remains in the cumulative N2 test.
func TestRealPVFN2PublicOrdinaryEntry(t *testing.T) {
	for _, id := range []int64{4953, 4978} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			ctx := context.Background()
			path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
			if path == "" {
				t.Skip("requires current real PVF")
			}
			archive, err := platformpvf.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			table, err := worldmap.LoadSource(ctx, archive, worldmap.Options{})
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
			ais, err := newPVFDungeonAICharacterCatalog(archive)
			if err != nil {
				t.Fatal(err)
			}
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 90,
				Stats: map[string]int64{"fatigue": 156, "town_id": 38, "area_id": 1,
					"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			s := &Service{
				options:                 options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"},
				initialEquipmentArchive: archive,
				worldMapTable:           table, worldMapResolver: resolver,
				dungeonMonsterTable: monsters, dungeonAICharacterTable: ais,
				dungeonChoice:      func(int) (int, error) { return 0, nil },
				dungeonSeed:        func() (uint32, error) { return 1, nil },
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}
			p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, channel: channelcatalog.Channel{ID: 42, Type: 11}}
			// Ordinary-handler fixture, matching the public master entry tests.
			// The town origin is fixture state; dungeon/map data is unmodified PVF.
			// This exercises resource projection/loading/movement, not raid admission.
			bindDungeonSelectorOriginForTestAt(t, s, p, 38, 1, 450, 234)
			body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id)})
			if err = s.handleDungeonSelectUpper(p, body); err != nil {
				t.Fatal(err)
			}
			r := p.dungeon.runtime
			if r == nil {
				t.Fatal("entry runtime absent")
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
				t.Fatalf("op45 boss map got=%d want=%d", bossScene.Map.Map.ID, want+2)
			}
			t.Logf("op45 reached declared boss map=%d via PVF gate=%v", bossScene.Map.Map.ID, gate)
			if err = os.WriteFile(filepath.Join(out, fmt.Sprintf("move-%d.bin", id)), p.conn.(*bufferConn).write.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
