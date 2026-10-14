package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

// Both ordinary routes used to reuse actor/drop IDs; the longer route then
// stopped on the sixth room because its drop batch reused an earlier key.
func TestRealPVFGrandineRoomRoutesKeepPassiveDropIdentity(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("matching real PVF required")
	}
	ctx := context.Background()
	a, e := platformpvf.Open(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if e != nil {
		t.Fatal(e)
	}
	table, e := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	mons, e := newPVFDungeonMonsterCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	ais, e := newPVFDungeonAICharacterCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	paths := [][]worldmap.RoomCoordinate{
		{{X: 1, Y: 0}, {X: 2, Y: 0}, {X: 2, Y: 1}, {X: 2, Y: 2}, {X: 3, Y: 2}, {X: 3, Y: 1}},
		{{X: 1, Y: 0}, {X: 2, Y: 0}, {X: 2, Y: 1}, {X: 1, Y: 1}, {X: 1, Y: 2}, {X: 2, Y: 2}, {X: 3, Y: 2}, {X: 3, Y: 1}},
	}
	for index, path := range paths {
		t.Run(string(rune('0'+index)), func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "a", Level: 90, Stats: map[string]int64{"fatigue": 156}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			s := &Service{options: options{accountID: "a", gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperHeader: gameUpperHeaderChannel13}, initialEquipmentArchive: a, worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: mons, dungeonAICharacterTable: ais, pvfItemCatalog: items, dungeonChoice: func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil }, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99}
			rt, _, e := s.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 104})
			if e != nil {
				t.Fatal(e)
			}
			session.dungeon.runtime = rt
			t.Logf("maze=%d", rt.MazeIndex)
			for step, next := range path {
				if step == 0 {
					if _, e := rt.Room.AnnounceAllActors(rt.Session); e != nil {
						t.Fatal(e)
					}
				}
				scene, _ := rt.Session.Scene()
				for key := range scene.RuntimeObjects {
					if _, _, e := rt.Room.CommitActorDeathReport(key, rt.Session); e != nil {
						t.Fatalf("death %d: %v", key, e)
					}
				}
				req := dungeoncmd.MoveMapRequest{NextX: byte(next.X), NextY: byte(next.Y)}
				plan, e := s.planCurrentDungeonMove(rt, req)
				if e != nil {
					t.Fatalf("target=%s: %v", next, e)
				}
				b, e := buildCurrentDungeonMoveStartMapBody(rt, plan)
				if e != nil {
					t.Fatal(e)
				}
				t.Logf("to=%s map=%d wireMap=%d bytes=%d", next, plan.Target.Map.Map.ID, binary.LittleEndian.Uint32(b[14:18]), len(b))
				body := make([]byte, dungeoncmd.MoveMapRequestSize)
				body[0] = byte(next.X)
				body[1] = byte(next.Y)
				conn.write.Reset()
				if e := s.handleDungeonMoveMap(session, body); e != nil {
					t.Fatal(e)
				}
				sc, _ := rt.Session.Scene()
				if sc.Coordinate != next {
					t.Fatalf("move silently blocked: %s", sc.Coordinate)
				}
				if rt.NextObjectKey < plan.NextObjectKey {
					t.Fatal("move rewound allocator behind actors")
				}
				rows, _ := currentDungeonPassiveObjectDropEntries(rt, sc)
				seen := map[uint32]bool{}
				for key := range sc.RuntimeObjects {
					seen[key] = true
				}
				for _, row := range rows {
					if seen[row.GlobalSequence] {
						t.Errorf("map=%d room=%s passive key=%d collides with actor", plan.Target.Map.Map.ID, sc.Coordinate, row.GlobalSequence)
					}
					seen[row.GlobalSequence] = true
					if row.GlobalSequence >= rt.NextObjectKey {
						t.Errorf("allocator %d behind passive key %d", rt.NextObjectKey, row.GlobalSequence)
					}
				}

				// A repeated room projection must reuse the original passive-drop ledger.
				count := len(rt.DropOwner.byObjectKey)
				if err := s.prepareCurrentDungeonPassiveObjectDrops(session, rt, sc, "rebuild"); err != nil {
					t.Fatal(err)
				}
				again, _ := currentDungeonPassiveObjectDropEntries(rt, sc)
				if len(rt.DropOwner.byObjectKey) != count || len(again) != len(rows) {
					t.Fatal("room rebuild minted passive drops")
				}
				for i := range rows {
					if rows[i].GlobalSequence != again[i].GlobalSequence {
						t.Fatal("room rebuild changed passive identity")
					}
				}
			}
			scene, _ := rt.Session.Scene()
			if !scene.Boss {
				t.Fatal("route did not reach boss")
			}
		})
	}
}
