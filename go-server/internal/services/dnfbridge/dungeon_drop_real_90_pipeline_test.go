package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func real90DropArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	name := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("需要DNF_WORLDMAP_REAL_PVF_SMOKE指定真实90版PVF")
	}
	a, err := platformpvf.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	// 生产进程只持有现用归档；真实资源回归会反复打开隔离归档。
	// 用例结束后解除自己的静态缓存引用，不能让后续用例累积整份PVF。
	t.Cleanup(func() {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		releaseReal90DropArchiveCaches(a)
		runtime.GC()
		runtime.ReadMemStats(&after)
		t.Logf("真实PVF用例释放自身归档缓存：堆占用%d→%d MiB", before.HeapAlloc>>20, after.HeapAlloc>>20)
	})
	return a
}

func releaseReal90DropArchiveCaches(archive *platformpvf.Archive) {
	var ownsSource func(dnfpvf.Source) bool
	ownsSource = func(source dnfpvf.Source) bool {
		switch value := source.(type) {
		case *platformpvf.Archive:
			return value == archive
		case *dungeonGenericPoolReadProbe:
			return value != nil && ownsSource(value.source)
		}
		return false
	}
	for _, cache := range []*sync.Map{
		&currentAbyssEpicPieceLevelIndexes, &currentDungeonGoldReferencesByCatalog,
		&currentDungeonMonsterDropRulesByCatalog, &currentDungeonDimensionDropsByMonsterCatalog,
		&currentDungeonSpecialDropsByMonsterCatalog, &currentDungeonMonsterExperienceResourcesByCatalog,
		&currentDungeonMonsterExperienceSourcesByCatalog, &currentEpicPieceCatalogs,
		&currentEpicPieceCatalogUnavailableLogged,
	} {
		cache.Range(func(key, _ any) bool {
			if catalog, ok := key.(*pvfDungeonMonsterCatalog); ok && ownsSource(catalog.source) {
				cache.Delete(key)
			}
			return true
		})
	}
	archive.ReleaseReadCaches()
}

// 采用实际表、实际抽取和119字节物品投影，资产仅落隔离内存仓储。
func TestRealPVF90CardsAndLegendaryDropsReachInventory(t *testing.T) {
	a := real90DropArchive(t)
	special, err := loadCurrentDungeonSpecialDrops(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		monster int64
		item    uint32
	}{{907, 3610}, {61236, 3726}, {63762, 100322226}} {
		t.Run(strconv.FormatUint(uint64(tc.item), 10), func(t *testing.T) {
			var seed uint32
			for candidate := uint32(1); candidate < 1000000; candidate++ {
				for _, id := range currentDungeonIndependentDropItems(special, tc.monster, 0, 85, 0, newCurrentDungeonDropLCG(candidate)) {
					if id == tc.item {
						seed = candidate
						break
					}
				}
				if seed != 0 {
					break
				}
			}
			if seed == 0 {
				t.Fatalf("真实独立掉落未能产出：怪物%d 物品%d", tc.monster, tc.item)
			}
			s, r, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
			s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
			if err != nil {
				t.Fatal(err)
			}
			items, err := s.dungeonMonsterTable.DropCatalog()
			if err != nil {
				t.Fatal(err)
			}
			definition, err := items.ResolveItem(tc.item)
			if err != nil {
				t.Fatal(err)
			}
			state, err := currentDungeonDeathDropItemState(1, definition, 1)
			if err != nil {
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(state.data[2:6]) != tc.item {
				t.Fatal("地面物品投影编号改变")
			}
			scene, _ := r.Session.Scene()
			r.DropOwner = newRuntimeDungeonDropOwner()
			drop := &runtimeDungeonDrop{ObjectKey: 600, SceneSlot: 1, Item: definition, Amount: 1, OwnerActorObjectKey: 99, Room: runtimeDungeonRoomKeyFromScene(scene), Status: runtimeDungeonDropAvailable}
			if definition.Kind == dungeonDropItemEquipment {
				drop.QualitySeed = binary.LittleEndian.Uint32(state.data[6:10])
			}
			if err := r.DropOwner.registerBatch([]*runtimeDungeonDrop{drop}, 2); err != nil {
				t.Fatal(err)
			}
			session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(600)); err != nil {
				t.Fatal(err)
			}
			inventory, found, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatalf("库存读取失败：%v", err)
			}
			var total int64
			for _, stack := range inventory.Slots {
				if stack.ItemID == int64(tc.item) {
					total += stack.Count
				}
			}
			if total != 1 || drop.Status != runtimeDungeonDropConsumed {
				t.Fatalf("真实掉落入包失败：数量%d 状态%v", total, drop.Status)
			}
			t.Logf("怪物%d，物品%d，真实命中种子%d，地面投影及拾取入库通过", tc.monster, tc.item, seed)
		})
	}
}

func TestRealPVF90BlackEarth7170ProducesMaterialsAndCareerEquipment(t *testing.T) {
	a := real90DropArchive(t)
	s, r, _ := prepareDimensionDungeonDropTest(t)
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	r.Dungeon.ID = 7170
	r.Dungeon.Metadata.BasisLevel = worldmap.OptionalInt{Set: true, Value: 85}
	r.Character.Level = 90
	r.Character.Job = "14"
	r.Character.Stats["grow_type"] = 36
	r.Request.Difficulty = 2
	text, err := a.ReadText("map/DimensionCrack/Blackearth/312236_ozma_3rd.map")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("map/DimensionCrack/Blackearth/312236_ozma_3rd.map", text)
	if err != nil {
		t.Fatal(err)
	}
	m := worldmap.ParseMap(312236, "map/DimensionCrack/Blackearth/312236_ozma_3rd.map", doc)
	if len(m.Monsters) != 1 || m.Monsters[0].MonsterID != 107004994 {
		t.Fatalf("真实BOSS地图变化：%+v", m.Monsters)
	}
	monster := r.Room.Snapshot().Monsters[0]
	monster.Spawn = m.Monsters[0]
	scene, _ := r.Session.Scene()
	visit, err := r.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	dimension, err := currentDungeonDimensionDrops(s.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	pool := dimension.ChronicleByCareer[currentDungeonDimensionChronicleKey{Job: 14, GrowType: 4}]
	allowed := map[uint32]bool{}
	for _, ids := range [][]uint32{pool.Normal, pool.Set} {
		for _, id := range ids {
			allowed[id] = true
		}
	}
	foundEquipment := false
	for seed := uint32(1); seed <= 2000; seed++ {
		visit.DropRNG = seed
		wires, err := s.planCurrentDungeonMonsterDrops(r, monster, 99)
		if err != nil {
			t.Fatal(err)
		}
		count := map[uint32]int{}
		for _, wire := range wires {
			id := binary.LittleEndian.Uint32(wire.Item.data[2:6])
			count[id]++
			if allowed[id] {
				foundEquipment = true
			}
		}
		if count[3311] != 1 || count[10303569] != 1 {
			t.Fatalf("7170真实BOSS材料缺失：种子%d，产物%v", seed, count)
		}
		if foundEquipment {
			t.Logf("7170真实地图/怪物/PVF规划通过，职业装备命中种子%d", seed)
			break
		}
	}
	if !foundEquipment {
		t.Fatal("7170真实职业装备池未参与产出")
	}
}
