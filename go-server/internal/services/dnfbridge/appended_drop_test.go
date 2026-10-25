package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"testing"
)

func TestAppendedDimensionMaterialQuantitiesOnGround(t *testing.T) {
	for tier := 0; tier < 3; tier++ {
		for _, rank := range []string{"[champion]", "[super champion]", "[boss]"} {
			t.Run(fmt.Sprint(tier, rank), func(t *testing.T) {
				s, r, repos := prepareDimensionDungeonDropTest(t)
				source := s.dungeonMonsterTable.source.(bridgePVFSource)
				source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
				source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
				source[currentDungeonDimensionGatePath] = "[dimensiondungeon list]\n700 25 0\n[/dimensiondungeon list]\n"
				source[currentDungeonDimensionInfoPath] = fmt.Sprintf("[dimension dungeon info]\n%d 700\n[/dimension dungeon info]\n[dungeon difficulty condition info]\n1 70 2 80 3 85\n[/dungeon difficulty condition info]\n[drop item Info]\n0 0 3311 1 0 3311 1 1 10303569 2 0 3311 2 1 10303569\n[/drop item Info]\n", tier)
				source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n3311 `material/fragment.stk`\n10303569 `material/memory.stk`\n"
				for _, p := range []string{"stackable/material/fragment.stk", "stackable/material/memory.stk"} {
					source[p] = "[name]\n`material`\n[stackable type]\n`[material]`\n[stack limit]\n9999\n[attach type]\n`[free]`\n"
				}
				currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
				currentDungeonGoldReferencesByCatalog.Delete(s.dungeonMonsterTable)
				currentDungeonDimensionDropsByMonsterCatalog.Delete(s.dungeonMonsterTable)
				r.Room.monsters[0].Spawn.Rank = rank
				r.Room.monsters[0].Spawn.FixedDropCount = 4
				wires, err := s.planCurrentDungeonMonsterDrops(r, r.Room.Snapshot().Monsters[0], currentSceneActorObjectKey(99))
				if err != nil {
					t.Fatal(err)
				}
				got := map[uint32]uint32{}
				rows := map[uint32]int{}
				for _, w := range wires {
					id := binary.LittleEndian.Uint32(w.Item.data[2:6])
					if id == 3311 || id == 10303569 {
						got[id] += binary.LittleEndian.Uint32(w.Item.data[6:10])
						rows[id]++
					}
				}
				wantFragments, wantMemory := uint32(7), [3]uint32{0, 4, 5}[tier]
				if rank == "[boss]" {
					wantFragments = 28
					wantMemory = [3]uint32{0, 16, 28}[tier]
				}
				if got[3311] != wantFragments || got[10303569] != wantMemory || rows[3311] != 1 || rows[10303569] > 1 {
					t.Fatalf("ground rows %v amount %v want %d/%d", rows, got, wantFragments, wantMemory)
				}
				session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: r}}
				for _, w := range wires {
					id := binary.LittleEndian.Uint32(w.Item.data[2:6])
					if id != 3311 && id != 10303569 {
						continue
					}
					for retry := 0; retry < 2; retry++ {
						if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(w.SceneObjectKey)); err != nil {
							t.Fatal("pickup", id, retry, err)
						}
					}
				}
				bag, found, err := repos.Inventory.Load(context.Background(), "99")
				if err != nil || !found {
					t.Fatal(found, err)
				}
				picked := map[int64]int64{}
				for _, stack := range bag.Slots {
					picked[stack.ItemID] += stack.Count
				}
				if picked[3311] != int64(wantFragments) || picked[10303569] != int64(wantMemory) {
					t.Fatalf("pickup/replay amounts %v, want %d/%d", picked, wantFragments, wantMemory)
				}
			})
		}
	}
}

func TestAppendedEpicRoadBossDeathPickupAndReplay(t *testing.T) {
	for _, id := range []uint32{6063, 6064, 6065} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, a, _, g, _ := prepareAuditActual6063Owner(t)
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewTextHandler(&logs, nil))
			packetLog, packetPath := testCharacterOptionPacketLogger(t)
			s.packetLog = packetLog
			t.Cleanup(func() {
				packetLog.Close()
				if t.Failed() {
					t.Log(logs.String())
					t.Log(readCharacterOptionPacketLog(t, packetPath))
				}
			})
			bag, _, _ := g.Inventory.Load(context.Background(), "99")
			stack := bag.Slots["0:1"]
			stack.Count = 90
			bag.Slots["0:1"] = stack
			if err := g.Inventory.Save(context.Background(), bag); err != nil {
				t.Fatal(err)
			}
			r, _, err := s.prepareDungeonRuntime(context.Background(), a, dungeoncmd.SelectDungeonRequest{DungeonID: id})
			if err != nil {
				t.Fatal(err)
			}
			if r.riftLoot == nil {
				t.Fatal("real admission missing event loot")
			}
			// Rate policy is tested against actual PVF separately. Force a hit here
			// to test the actual ordinary-boss handler and durable pickup path.
			r.riftLoot.EpicRate = 1
			if err = freezeCurrentDungeonTownReturnOrigin(a, r); err != nil {
				t.Fatal(err)
			}
			if err = s.commitDungeonRuntime(a, r); err != nil {
				t.Fatal(err)
			}
			pool := map[uint32]bool{}
			for _, item := range r.riftLoot.Equipment {
				pool[item] = true
			}
			for x := 0; x < 3; x++ {
				if x > 0 {
					scene, e := r.Session.MoveByte(byte(x), 0)
					if e != nil {
						t.Fatal(e)
					}
					room, next, e := newRuntimeDungeonRoom(scene, s.dungeonMonsterTable, r.NextObjectKey)
					if e != nil {
						t.Fatal(e)
					}
					r.Room = room
					r.NextObjectKey = next
					if err = r.cacheCurrentDungeonRoom(); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = r.Room.AnnounceAllActors(r.Session); err != nil {
					t.Fatal(err)
				}
				for _, m := range r.Room.Snapshot().Monsters {
					if m.State == runtimeDungeonMonsterDefeated {
						continue
					}
					if _, already := r.Room.DefeatedDeathTarget(m.ObjectKey); already {
						continue
					}
					body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
					binary.LittleEndian.PutUint32(body, m.ObjectKey)
					binary.LittleEndian.PutUint16(body[4:], 99)
					body[30] = 1
					if err = s.handleDungeonMonsterDeath(a, body); err != nil {
						t.Fatal("death", x, m.Definition.ID, err)
					}
					before := 0
					if r.DropOwner != nil {
						before = len(r.DropOwner.byObjectKey)
					}
					if err = s.handleDungeonMonsterDeath(a, body); err != nil {
						t.Fatal("replay", err)
					}
					if r.DropOwner != nil && len(r.DropOwner.byObjectKey) != before {
						t.Fatal("duplicate death rerolled")
					}
				}
			}
			if r.DropOwner == nil {
				t.Fatal("no drop owner")
			}
			count := 0
			for key, drop := range r.DropOwner.byObjectKey {
				if !pool[drop.Item.ItemID] {
					continue
				}
				count++
				for retry := 0; retry < 2; retry++ {
					if err = s.handleCurrentDungeonPickup(a, currentDungeonPickupTestBody(key)); err != nil {
						t.Fatal(err)
					}
				}
				if drop.Status != runtimeDungeonDropConsumed {
					t.Fatal("event epic not pickable")
				}
			}
			if count != 1 {
				t.Fatal("expected one event epic per boss", count)
			}
			bag, _, err = g.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			total := int64(0)
			for _, stack := range bag.Slots {
				if pool[uint32(stack.ItemID)] {
					total += stack.Count
				}
			}
			if total != 1 {
				t.Fatal("inventory/replay", total)
			}
		})
	}
}

func TestAppendedEpicRoadActualPoolsAndRate(t *testing.T) {
	s, _, _, _, _ := prepareAuditActual6063Owner(t)
	for tier, id := range []int64{6063, 6064, 6065} {
		d, err := s.prepareCurrentEpicRoadLoot(id)
		if err != nil {
			t.Fatal(id, err)
		}
		wantRate := [...]float64{0.10, 0.15, 0.20}[tier]
		if tickets, ok := dnfdungeon.EpicEntryTicketCount(id); !ok || tickets != [...]int64{30, 45, 60}[tier] {
			t.Fatal("probability tier does not match paid entry", id, tickets)
		}
		if len(d.Equipment) < 200 || d.EpicRate != wantRate || d.FragmentCount != 0 || d.MultiEpicCount {
			t.Fatal(id, "incorrect policy", len(d.Equipment), d.EpicRate)
		}
		allowed := map[uint32]bool{}
		for _, i := range d.Equipment {
			allowed[i] = true
		}
		hits := 0
		rng := newCurrentDungeonDropLCG(1)
		for i := 0; i < 10000; i++ {
			items := d.selectItems(rng)
			if len(items) > 1 {
				t.Fatal("more than one trial", items)
			}
			for _, item := range items {
				if !allowed[item.ItemID] || item.Amount != 1 {
					t.Fatal("foreign drop", item)
				}
				hits++
			}
		}
		wantHits := int(wantRate * 10000)
		if hits < wantHits-150 || hits > wantHits+150 {
			t.Fatal(id, "unexpected tier probability sample", hits, wantRate)
		}
		t.Logf("dungeon %d exact pool %d, actual rate %.2f, sample %d/10000", id, len(d.Equipment), d.EpicRate, hits)
	}
	if _, err := s.prepareCurrentEpicRoadLoot(6062); err == nil {
		t.Fatal("free forest borrowed paid event loot")
	}
}
