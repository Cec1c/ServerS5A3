package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestDimensionImageEntryCatalogAndRankIsolation(t *testing.T) {
	catalog := &currentDungeonDimensionDropCatalog{ExactTiers: map[int64]int{7167: 2}, ItemsByTier: map[int][]currentDungeonDimensionDropEntry{0: {{ItemID: 3311}}, 1: {{ItemID: 3311}}, 2: {{ItemID: 3311}}}}
	r := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 7100, Metadata: worldmap.DungeonMetadata{Flags: map[string]bool{"dimension dungeon": true}}, Extensions: []worldmap.ExtensionSection{{Name: "entry dungeon"}}}}
	for tier := 0; tier < 3; tier++ {
		r.Request.Difficulty = byte(tier)
		view, err := currentRuntimeDimensionDrops(catalog, r)
		if err != nil || view.ExactTiers[7100] != tier {
			t.Fatal(view, err)
		}
		if _, ok := catalog.ExactTiers[7100]; ok {
			t.Fatal("shared catalog mutated")
		}
	}
	r.Request.Difficulty = 3
	if _, err := currentRuntimeDimensionDrops(catalog, r); err == nil {
		t.Fatal("invalid tier accepted")
	}
	r.Dungeon.Metadata.Flags["dimension dungeon"] = false
	if view, err := currentRuntimeDimensionDrops(catalog, r); err != nil || view != catalog {
		t.Fatal("unrelated dungeon changed")
	}
	m := runtimeDungeonMonster{Spawn: worldmap.MonsterSpawn{Rank: "[normal]"}}
	m.Definition.Scalars = map[string]float64{"named_monster": 1}
	if rank := currentDimensionRewardRank(r, m, 0); rank != 0 {
		t.Fatal("non-dimension named affected")
	}
	r.Dungeon.Metadata.Flags["dimension dungeon"] = true
	if rank := currentDimensionRewardRank(r, m, 0); rank != 1 {
		t.Fatal("named flag ignored")
	}
	m.Spawn.SuffixMarker = "[boss]"
	if rank := currentDimensionRewardRank(r, m, 0); rank != 3 {
		t.Fatal("boss suffix not preferred")
	}
	m.Spawn.SuffixMarker = ""
	m.Definition.Scalars["named_monster"] = 0
	if rank := currentDimensionRewardRank(r, m, 0); rank != 0 {
		t.Fatal("explicit zero named flag promoted")
	}
}

func TestDimensionImageActualEntryDeathReplay(t *testing.T) {
	for tier := 0; tier < 3; tier++ {
		t.Run(fmt.Sprint(tier), func(t *testing.T) {
			s, a, _, repos, _ := prepareAuditActual6063Owner(t)
			ctx := context.Background()
			character, _, err := repos.Character.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			character.Job = "0"
			character.Stats["grow_type"] = 1
			character.Stats["town_id"] = 107
			character.Stats["area_id"] = 0
			if err = repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			bag, _, err := repos.Inventory.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			stack := bag.Slots["0:1"]
			stack.ItemID = 3332
			stack.Count = 30
			bag.Slots["0:1"] = stack
			if err = repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			bindDungeonSelectorOriginForTestAt(t, s, a, 107, 0, 250, 224)
			r, _, err := s.prepareDungeonRuntime(ctx, a, dungeoncmd.SelectDungeonRequest{DungeonID: 7100, Difficulty: byte(tier)})
			if err != nil {
				t.Fatal(err)
			}
			if r.Dungeon.ID != 7100 || int(r.Request.Difficulty) != tier {
				t.Fatal("entry replaced", r.Dungeon.ID, r.Request)
			}
			if err = freezeCurrentDungeonTownReturnOrigin(a, r); err != nil {
				t.Fatal(err)
			}
			if err = s.commitDungeonRuntime(a, r); err != nil {
				t.Fatal(err)
			}
			eliteDeaths, bossDeaths := 0, 0
			for x := 0; x <= 5; x++ {
				// Clear every room through the actual death handler before advancing.
				if x > 0 {
					scene, e := r.Session.MoveByte(byte(x), 0)
					if e != nil {
						t.Fatal(e)
					}
					room, next, e := newRuntimeDungeonRoom(scene, s.dungeonMonsterTable, r.NextObjectKey)
					if e != nil {
						t.Fatal(e)
					}
					r.Room, r.NextObjectKey = room, next
					if e = r.cacheCurrentDungeonRoom(); e != nil {
						t.Fatal(e)
					}
				}
				var e error
				if _, e = r.Room.AnnounceAllActors(r.Session); e != nil {
					t.Fatal(e)
				}
				for _, monster := range r.Room.Snapshot().Monsters {
					if _, defeated := r.Room.DefeatedDeathTarget(monster.ObjectKey); defeated {
						continue
					}
					spawnRank, e := currentDungeonMonsterType(monster.Spawn.Rank)
					if e != nil {
						t.Fatal(e)
					}
					rank := currentDimensionRewardRank(r, monster, spawnRank)
					body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
					binary.LittleEndian.PutUint32(body, monster.ObjectKey)
					binary.LittleEndian.PutUint16(body[4:], 99)
					body[30] = 1
					before := map[uint32]bool{}
					if r.DropOwner != nil {
						for key := range r.DropOwner.byObjectKey {
							before[key] = true
						}
					}
					if e = s.handleDungeonMonsterDeath(a, body); e != nil {
						t.Fatal(e)
					}
					if rank > 0 && (r.DropOwner == nil || len(r.DropOwner.byObjectKey) <= len(before)) {
						t.Fatal("death produced no drops")
					}
					if r.DropOwner == nil {
						continue
					}
					after := len(r.DropOwner.byObjectKey)
					if e = s.handleDungeonMonsterDeath(a, body); e != nil {
						t.Fatal(e)
					}
					if len(r.DropOwner.byObjectKey) != after {
						t.Fatal("death replay rerolled")
					}
					got := map[uint32]uint32{}
					equipment := 0
					for key, drop := range r.DropOwner.byObjectKey {
						if before[key] {
							continue
						}
						if drop.Item.ItemID == 3311 || drop.Item.ItemID == 10303569 {
							got[drop.Item.ItemID] += drop.Amount
						}
						if drop.Item.Kind == dungeonDropItemEquipment && drop.Item.Rarity == 5 {
							equipment++
						}
						for retry := 0; retry < 2; retry++ {
							if e = s.handleCurrentDungeonPickup(a, currentDungeonPickupTestBody(key)); e != nil {
								t.Fatal(e)
							}
						}
					}
					if rank == 0 {
						continue
					}
					wantFragment, wantMemory := uint32(7), [3]uint32{0, 4, 5}[tier]
					if rank == 3 {
						bossDeaths++
						wantFragment = 28
						wantMemory = [3]uint32{0, 16, 28}[tier]
					} else {
						eliteDeaths++
					}
					if got[3311] != wantFragment || got[10303569] != wantMemory || equipment != 1 {
						t.Fatal("death quantities", x, got, equipment)
					}
				}
			}
			if eliteDeaths == 0 || bossDeaths != 1 {
				t.Fatal("missing special deaths", eliteDeaths, bossDeaths)
			}
		})
	}
}

func TestDimensionImageRealProbabilityAudit(t *testing.T) {
	a := real90DropArchive(t)
	catalog, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	dimension, err := currentDungeonDimensionDrops(catalog)
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	special, err := currentDungeonSpecialDrops(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for tier, rate := range dimension.ChronicleRates {
		t.Logf("tier=%d cardReward=%d%% setSelection=%d%% (monster equipment guaranteed by image rule)", tier, rate.RewardPercent, rate.SetPercent)
	}
	for _, id := range []uint32{3311, 10303569, 1254, 1255, 1256, 10090758, 10090759, 10090760} {
		def, err := items.ResolveItem(id)
		if err == nil {
			t.Logf("item=%d name=%s", id, def.Name)
		}
	}
	for _, id := range []int64{61340, 61809, 107000259, 107000260, 107000261, 107004994} {
		pool, err := items.MonsterPool(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("monster=%d explicitPool=%+v independent=%+v", id, pool, special.IndependentByMonster[id])
	}
}
