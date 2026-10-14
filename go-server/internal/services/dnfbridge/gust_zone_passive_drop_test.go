package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The matching PVF repeats an identical group in the ordinary and alternate
// maze declarations. Both ordinary routes must still enter the boss room.
func TestRealPVFGustZoneBossRoutesAndPassiveDrops(t *testing.T) {
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
		{{X: 4, Y: 1}, {X: 3, Y: 1}, {X: 2, Y: 1}, {X: 1, Y: 1}, {X: 0, Y: 1}, {X: 0, Y: 2}, {X: 1, Y: 2}},
		{{X: 4, Y: 1}, {X: 3, Y: 1}, {X: 3, Y: 2}, {X: 3, Y: 3}, {X: 2, Y: 3}, {X: 1, Y: 3}, {X: 1, Y: 2}},
	}
	for index, path := range paths {
		t.Run(string(rune('0'+index)), func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			if e := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "99", AccountID: "a", Level: 90, Stats: map[string]int64{"fatigue": 156}}); e != nil {
				t.Fatal(e)
			}
			if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); e != nil {
				t.Fatal(e)
			}
			s := &Service{
				options:                 options{accountID: "a", gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperHeader: gameUpperHeaderChannel13},
				initialEquipmentArchive: a,
				worldMapTable:           table, worldMapResolver: resolver,
				dungeonMonsterTable: mons, dungeonAICharacterTable: ais, pvfItemCatalog: items,
				dungeonChoice:      func(int) (int, error) { return 0, nil },
				dungeonSeed:        func() (uint32, error) { return 1, nil },
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 99}
			rt, _, e := s.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 313})
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
				if index == 1 && step == len(path)-1 {
					before, _ := rt.Session.Scene()
					writeErr := errors.New("injected boss START_MAP write failure")
					session.conn = &failNthDungeonWriteConn{failAt: 1, err: writeErr}
					if err := s.handleDungeonMoveMap(session, body); !errors.Is(err, writeErr) {
						t.Fatalf("missing write failure: %v", err)
					}
					after, _ := rt.Session.Scene()
					if before.Coordinate != after.Coordinate {
						t.Fatal("failed packet moved the character")
					}
					session.conn = conn
				}
				conn.write.Reset()
				if e := s.handleDungeonMoveMap(session, body); e != nil {
					t.Fatal(e)
				}
				sc, _ := rt.Session.Scene()
				if sc.Coordinate != next {
					t.Fatalf("move to %s silently blocked at %s", next, sc.Coordinate)
				}
				if sc.Boss {
					packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
					if packet.Header.MsgID != currentDungeonStartNotification || len(rest) != 0 || len(packet.Body) != 128 || binary.LittleEndian.Uint32(packet.Body[14:18]) != 33753 || packet.Body[40] != 4 {
						t.Fatalf("boss START_MAP header=%+v bytes=%d rest=%d", packet.Header, len(packet.Body), len(rest))
					}
					rows, _ := currentDungeonPassiveObjectDropEntries(rt, sc)
					if len(rows) != 4 {
						t.Fatalf("boss passive rows=%d", len(rows))
					}
					seen := map[uint32]bool{}
					for key := range sc.RuntimeObjects {
						seen[key] = true
					}
					for rowIndex, row := range rows {
						if row.ItemID != 10158700 || row.StackCount != 1 {
							t.Fatalf("unexpected passive reward: %+v", row)
						}
						if seen[row.GlobalSequence] {
							t.Fatalf("passive drop key %d collides with another scene object", row.GlobalSequence)
						}
						seen[row.GlobalSequence] = true
						offset := 41 + rowIndex*21
						if binary.LittleEndian.Uint32(packet.Body[offset+1:offset+5]) != row.GlobalSequence || binary.LittleEndian.Uint32(packet.Body[offset+5:offset+9]) != row.ItemID {
							t.Fatal("passive reward wire identity differs from owner")
						}
						if row.GlobalSequence >= rt.NextObjectKey {
							t.Fatal("object allocator did not advance beyond drops")
						}
					}
					if err := s.prepareCurrentDungeonPassiveObjectDrops(session, rt, sc, "rebuild"); err != nil {
						t.Fatal(err)
					}
					again, _ := currentDungeonPassiveObjectDropEntries(rt, sc)
					if len(again) != 4 || len(rt.DropOwner.byObjectKey) != 4 {
						t.Fatal("rebuild minted duplicate passive drops")
					}
					for attempt := 0; attempt < 2; attempt++ {
						conn.write.Reset()
						if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(rows[0].GlobalSequence)); err != nil {
							t.Fatal(err)
						}
					}
					inventory, found, err := repos.Inventory.Load(ctx, "99")
					if err != nil || !found {
						t.Fatalf("load pickup: %v", err)
					}
					assertCurrentNPCShopInventoryAmount(t, inventory, 10158700, 1)
					remaining, _ := currentDungeonPassiveObjectDropEntries(rt, sc)
					if len(remaining) != 3 {
						t.Fatal("picked up stone was still projected")
					}
				}
			}
		})
	}
}
