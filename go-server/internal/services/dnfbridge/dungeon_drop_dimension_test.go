package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentDungeonMonsterPlanUsesPVFDimensionMembershipAsOrdinaryPool(t *testing.T) {
	service, runtime, _ := prepareDimensionDungeonDropTest(t)
	source := service.dungeonMonsterTable.source.(bridgePVFSource)
	source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	source[currentDungeonDimensionGatePath] = "[dimensiondungeon list]\n700 25 0\n[/dimensiondungeon list]\n"
	source[currentDungeonDimensionInfoPath] = `[dimension dungeon info]
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70 2 80 3 85
[/dungeon difficulty condition info]
[drop item Info]
0 0 3228
0 1 3229
[/drop item Info]
`
	source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n3228 `material/test_dimension_drop.stk`\n3229 `material/test_dimension_drop_2.stk`\n"
	source["stackable/material/test_dimension_drop.stk"] = "[name]\n`Test Dimension Drop`\n[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	source["stackable/material/test_dimension_drop_2.stk"] = "[name]\n`Test Dimension Drop 2`\n[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonDimensionDropsByMonsterCatalog.Delete(service.dungeonMonsterTable)
	monster := runtime.Room.Snapshot().Monsters[0]
	wires, err := service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(99))
	if err != nil {
		t.Fatal(err)
	}
	dimensionRows := 0
	for _, wire := range wires {
		itemID := binary.LittleEndian.Uint32(wire.Item.data[0x02:0x06])
		if itemID == 3228 || itemID == 3229 {
			dimensionRows++
		}
	}
	if dimensionRows > 1 {
		t.Fatalf("one ordinary-pool attempt emitted %d dimension rows; materials were appended directly: wires=%d", dimensionRows, len(wires))
	}
}

func TestCurrentDungeonMonsterPlanKeepsDimensionFragmentsForSpecialRanks(t *testing.T) {
	for _, rank := range []string{"[champion]", "[super champion]", "[boss]"} {
		t.Run(rank, func(t *testing.T) {
			service, runtime, _ := prepareDimensionDungeonDropTest(t)
			source := service.dungeonMonsterTable.source.(bridgePVFSource)
			source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
			source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
			source[currentDungeonDimensionGatePath] = "[dimensiondungeon list]\n700 25 0\n[/dimensiondungeon list]\n"
			source[currentDungeonDimensionInfoPath] = `[dimension dungeon info]
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70 2 80 3 85
[/dungeon difficulty condition info]
[drop item Info]
0 0 3228
0 1 3229
[/drop item Info]
`
			source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n3228 `material/test_dimension_drop.stk`\n3229 `material/test_dimension_drop_2.stk`\n"
			source["stackable/material/test_dimension_drop.stk"] = "[name]\n`Test Dimension Drop`\n[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
			source["stackable/material/test_dimension_drop_2.stk"] = "[name]\n`Test Dimension Drop 2`\n[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
			currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
			currentDungeonGoldReferencesByCatalog.Delete(service.dungeonMonsterTable)
			currentDungeonDimensionDropsByMonsterCatalog.Delete(service.dungeonMonsterTable)

			runtime.Room.mu.Lock()
			runtime.Room.monsters[0].Spawn.Rank = rank
			runtime.Room.mu.Unlock()
			monster := runtime.Room.Snapshot().Monsters[0]
			wires, err := service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(99))
			if err != nil {
				t.Fatal(err)
			}
			dimensionRows := 0
			for _, wire := range wires {
				itemID := binary.LittleEndian.Uint32(wire.Item.data[0x02:0x06])
				if itemID == 3228 || itemID == 3229 {
					dimensionRows++
				}
			}
			if dimensionRows != 2 {
				t.Fatalf("special rank %s emitted %d dimension rows, want both PVF tier/group rows: wires=%d", rank, dimensionRows, len(wires))
			}
		})
	}
}

func TestCurrentDungeonMonsterPlanMapsEquipmentRollToCareerChroniclePool(t *testing.T) {
	service, runtime, _ := prepareDimensionDungeonDropTest(t)
	source := service.dungeonMonsterTable.source.(bridgePVFSource)
	rules := currentDungeonGoldDropTestRuleText()
	rules = strings.Replace(rules, "1 200 10000 0 0 0 0", "1 200 0 0 10000 0 0", 1)
	rules = strings.Replace(rules, "[item drop ref table]\n1 0 3", "[item drop ref table]\n20 0 3", 1)
	source[dungeonCardDropRulePath] = rules
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	source[currentDungeonDimensionGatePath] = "[dimensiondungeon list]\n700 25 0\n[/dimensiondungeon list]\n"
	source[currentDungeonDimensionInfoPath] = `[dimension dungeon info]
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70 2 80 3 85
[/dungeon difficulty condition info]
[drop item Info]
0 0 3228
[/drop item Info]
`
	source[currentDungeonDimensionDropListPath] = `[chronicle grow type]
0 1
[normal chronicle list]
9101 9102
[/normal chronicle list]
[set chronicle list]
9201
[/set chronicle list]
[/chronicle grow type]
`
	source[currentDungeonDimensionDropExcludePath] = "[exclude list]\n9102\n[/exclude list]\n"
	source[currentDungeonClearRewardRulePath] = "[chronicle set reward rate]\n0 50 0\n[/chronicle set reward rate]\n"
	source[dungeonDropStackableList] = "3227 `material/test_drop.stk`\n3228 `material/test_dimension_drop.stk`\n"
	source["stackable/material/test_dimension_drop.stk"] = "[name]\n`Test Dimension Drop`\n[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	source[dungeonDropEquipmentList] = "9101 `chronicle/test_normal.equ`\n9102 `chronicle/test_excluded.equ`\n9201 `chronicle/test_set.equ`\n"
	for _, itemPath := range []string{"equipment/chronicle/test_normal.equ", "equipment/chronicle/test_excluded.equ", "equipment/chronicle/test_set.equ"} {
		source[itemPath] = "[name]\n`Test Chronicle`\n[rarity]\n5\n[minimum level]\n20\n[durability]\n50\n[attach type]\n`[trade]`\n[equipment type]\n`[weapon]`\n"
	}
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(service.dungeonMonsterTable)
	currentDungeonDimensionDropsByMonsterCatalog.Delete(service.dungeonMonsterTable)
	loadedRules, err := currentDungeonMonsterDropRules(service.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	if row, found := loadedRules.Probability(20); !found || row.Rates[2] != 10000 {
		t.Fatalf("equipment probability row=%+v found=%t", row, found)
	}
	if row, found := loadedRules.ItemReference(20); !found {
		t.Fatalf("equipment item-reference row for level 20 is missing")
	} else if row.ValueA != 0 || row.ValueB != 3 {
		t.Fatalf("equipment item-reference row=%+v", row)
	}
	dimensionCatalog, err := currentDungeonDimensionDrops(service.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	if pool := dimensionCatalog.ChronicleByCareer[currentDungeonDimensionChronicleKey{Job: 0, GrowType: 1}]; !reflect.DeepEqual(pool.Normal, []uint32{9101}) || !reflect.DeepEqual(pool.Set, []uint32{9201}) {
		t.Fatalf("career chronicle pool=%+v", pool)
	}

	runtime.Character.Job = "0"
	runtime.Character.Stats["grow_type"] = 1
	runtime.Room.mu.Lock()
	runtime.Room.monsters[0].Spawn.RandomDropCount = 1
	runtime.Room.monsters[0].Spawn.Rank = "[boss]"
	runtime.Room.mu.Unlock()
	monster := runtime.Room.Snapshot().Monsters[0]
	if monster.Spawn.RandomDropCount != 1 || monster.Spawn.Rank != "[boss]" {
		t.Fatalf("monster random_drop_count=%d rank=%q", monster.Spawn.RandomDropCount, monster.Spawn.Rank)
	}
	wires, err := service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(99))
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[uint32]int)
	for _, wire := range wires {
		counts[binary.LittleEndian.Uint32(wire.Item.data[0x02:0x06])]++
	}
	if counts[9101] != 1 || counts[9102] != 0 || counts[9201] != 0 {
		t.Fatalf("career chronicle equipment rows=%v", counts)
	}
	dimensionCatalog.ChronicleRates[0] = currentDungeonDimensionChronicleRate{RewardPercent: 100, SetPercent: 100}
	itemCatalog, err := service.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	card, found, err := currentDungeonRandomDimensionCardItem(
		newCurrentDungeonDropLCG(1),
		itemCatalog,
		dimensionCatalog,
		700,
		20,
		0,
		1,
		time.Unix(0, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || card.ItemID != 9201 || card.Stackable {
		t.Fatalf("career chronicle clear-card reward=%+v", card)
	}
}

func TestCurrentDungeonDimensionDropsAreDerivedFromPVF(t *testing.T) {
	source := bridgePVFSource{
		currentDungeonDimensionGatePath: `[dimensiondungeon list]
62 25 0 65 25 0 67 25 0
[/dimensiondungeon list]
`,
		currentDungeonDimensionInfoPath: `[dimension dungeon info]
0 7165 1 7166 2 7167
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70 2 80 3 85
[/dungeon difficulty condition info]
[drop item Info]
0 0 3311
1 0 3311
1 1 10303569
2 0 3311
2 1 10303569
[/drop item Info]
`,
	}
	catalog, err := loadCurrentDungeonDimensionDrops(source)
	if err != nil {
		t.Fatal(err)
	}

	tier, pool, matched, err := currentDungeonDimensionDropPool(catalog, 62, 90)
	if err != nil {
		t.Fatal(err)
	}
	want := []dungeonMonsterDropPoolEntry{{ItemID: 3311, Weight: 100}, {ItemID: 10303569, Weight: 100}}
	if !matched || tier != 2 || !reflect.DeepEqual(pool, want) {
		t.Fatalf("legacy dimension pool matched=%t tier=%d pool=%v want=%v", matched, tier, pool, want)
	}

	tier, pool, matched, err = currentDungeonDimensionDropPool(catalog, 7166, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || tier != 1 || !reflect.DeepEqual(pool, want) {
		t.Fatalf("exact dimension pool matched=%t tier=%d pool=%v", matched, tier, pool)
	}

	_, pool, matched, err = currentDungeonDimensionDropPool(catalog, 66, 90)
	if err != nil || matched || len(pool) != 0 {
		t.Fatalf("PVF-nonmember dungeon matched=%t pool=%v err=%v", matched, pool, err)
	}
}

func TestCurrentDungeonDimensionChronicleDropsUseCareerPoolsRatesAndExcludeList(t *testing.T) {
	source := bridgePVFSource{
		currentDungeonDimensionGatePath: `[dimensiondungeon list]
62 25 0
[/dimensiondungeon list]
`,
		currentDungeonDimensionInfoPath: `[dimension dungeon info]
0 7165 1 7166 2 7167
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70 2 80 3 85
[/dungeon difficulty condition info]
[drop item Info]
0 0 3311 1 0 3311 1 1 10303569 2 0 3311 2 1 10303569
[/drop item Info]
`,
		currentDungeonDimensionDropListPath: `[chronicle grow type]
0 1
[normal chronicle list]
125012 125013
[/normal chronicle list]
[set chronicle list]
120229 120230
[/set chronicle list]
[/chronicle grow type]
[chronicle grow type]
0 2
[normal chronicle list]
105000
[/normal chronicle list]
[set chronicle list]
100332
[/set chronicle list]
[/chronicle grow type]
`,
		currentDungeonDimensionDropExcludePath: `[exclude list]
125013 120230
[/exclude list]
`,
		currentDungeonClearRewardRulePath: `[chronicle set reward rate]
0 50 0 1 70 5 2 95 100
[/chronicle set reward rate]
`,
	}
	catalog, err := loadCurrentDungeonDimensionDrops(source)
	if err != nil {
		t.Fatal(err)
	}
	pool := catalog.ChronicleByCareer[currentDungeonDimensionChronicleKey{Job: 0, GrowType: 1}]
	if !reflect.DeepEqual(pool.Normal, []uint32{125012}) || !reflect.DeepEqual(pool.Set, []uint32{120229}) {
		t.Fatalf("filtered career pool=%+v", pool)
	}
	if got := catalog.ChronicleRates[2]; got != (currentDungeonDimensionChronicleRate{RewardPercent: 95, SetPercent: 100}) {
		t.Fatalf("tier-2 rate=%+v", got)
	}

	rng := newCurrentDungeonDropLCG(1)
	tier, itemID, group, matched, selected, err := currentDungeonDimensionChronicleItem(catalog, 7167, 90, 0, 1, rng, false)
	if err != nil || !matched || !selected || tier != 2 || itemID != 120229 || group != "set" {
		t.Fatalf("chronicle selection tier=%d item=%d group=%q matched=%t selected=%t err=%v", tier, itemID, group, matched, selected, err)
	}

	_, _, _, matched, selected, err = currentDungeonDimensionChronicleItem(catalog, 7167, 90, 0, 3, rng, false)
	if err != nil || !matched || selected {
		t.Fatalf("unknown career matched=%t selected=%t err=%v", matched, selected, err)
	}
}

func TestCurrentDungeonDimensionChroniclePVFShapeFailsClosed(t *testing.T) {
	base := bridgePVFSource{
		currentDungeonDimensionGatePath: "[dimensiondungeon list]\n62 25 0\n[/dimensiondungeon list]\n",
		currentDungeonDimensionInfoPath: `[dimension dungeon info]
[/dimension dungeon info]
[dungeon difficulty condition info]
1 70
[/dungeon difficulty condition info]
[drop item Info]
0 0 3311
[/drop item Info]
`,
		currentDungeonDimensionDropListPath: `[chronicle grow type]
0 1
[normal chronicle list]
125012
[/normal chronicle list]
[set chronicle list]
120229
[/set chronicle list]
[/chronicle grow type]
`,
		currentDungeonDimensionDropExcludePath: "[exclude list]\n[/exclude list]\n",
		currentDungeonClearRewardRulePath:      "[chronicle set reward rate]\n0 101 0\n[/chronicle set reward rate]\n",
	}
	if _, err := loadCurrentDungeonDimensionDrops(base); err == nil {
		t.Fatal("out-of-range chronicle reward rate was accepted")
	}

	delete(base, currentDungeonDimensionDropExcludePath)
	base[currentDungeonClearRewardRulePath] = "[chronicle set reward rate]\n0 50 0\n[/chronicle set reward rate]\n"
	if _, err := loadCurrentDungeonDimensionDrops(base); err == nil {
		t.Fatal("chronicle drop list without its exclusion table was accepted")
	}
}

func TestCurrentDungeonDimensionDropPVFShapeFailsClosed(t *testing.T) {
	_, err := loadCurrentDungeonDimensionDrops(bridgePVFSource{
		currentDungeonDimensionGatePath: "[dimensiondungeon list]\n62 25\n[/dimensiondungeon list]\n",
	})
	if err == nil {
		t.Fatal("malformed dimension-dungeon tuple was accepted")
	}

	catalog := &currentDungeonDimensionDropCatalog{
		LegacyDungeons: map[int64]struct{}{62: {}},
		ItemsByTier:    map[int][]currentDungeonDimensionDropEntry{},
	}
	if _, _, _, err := currentDungeonDimensionDropPool(catalog, 62, 90); err == nil {
		t.Fatal("dimension member without an item tier was accepted")
	}
}

func TestRealScriptPVFDimensionDungeonDropsResolve(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to audit real dimension-dungeon drops")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{}); err != nil {
		t.Fatal(err)
	}
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	dimensionCatalog, err := currentDungeonDimensionDrops(monsterCatalog)
	if err != nil {
		t.Fatal(err)
	}
	itemCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(dimensionCatalog.ChronicleByCareer) == 0 || len(dimensionCatalog.ChronicleRates) != 3 {
		t.Fatalf("chronicle career pools=%d rates=%v", len(dimensionCatalog.ChronicleByCareer), dimensionCatalog.ChronicleRates)
	}
	for key, pool := range dimensionCatalog.ChronicleByCareer {
		for group, entries := range map[string][]uint32{"normal": pool.Normal, "set": pool.Set} {
			for _, itemID := range entries {
				definition, resolveErr := itemCatalog.ResolveItem(itemID)
				if resolveErr != nil {
					t.Fatalf("chronicle job=%d grow=%d group=%s item=%d: %v", key.Job, key.GrowType, group, itemID, resolveErr)
				}
				if definition.Kind != dungeonDropItemEquipment || func() bool {
					doc, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
					if err != nil {
						t.Fatal(err)
					}
					rarity, _ := doc.Int("rarity")
					return rarity != 5
				}() {
					t.Fatalf("chronicle job=%d grow=%d group=%s item=%d kind=%s rarity=%s", key.Job, key.GrowType, group, itemID, definition.Kind, "PVF")
				}
			}
		}
	}
	for _, dungeonID := range []int64{62, 63, 64, 65, 67} {
		tier, pool, matched, resolveErr := currentDungeonDimensionDropPool(dimensionCatalog, dungeonID, 90)
		if resolveErr != nil || !matched || len(pool) == 0 {
			t.Fatalf("dungeon=%d matched=%t tier=%d pool=%v err=%v", dungeonID, matched, tier, pool, resolveErr)
		}
		for _, entry := range pool {
			if _, resolveErr := itemCatalog.ResolveItem(entry.ItemID); resolveErr != nil {
				t.Fatalf("dungeon=%d tier=%d item=%d: %v", dungeonID, tier, entry.ItemID, resolveErr)
			}
		}
	}
	if _, _, matched, err := currentDungeonDimensionDropPool(dimensionCatalog, 66, 90); err != nil || matched {
		t.Fatalf("current PVF intentionally omits dungeon 66 from dimension membership: matched=%t err=%v", matched, err)
	}
}

func prepareDimensionDungeonDropTest(t *testing.T) (*Service, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	s, r, repos := prepareCurrentDungeonDropTest(t, 1, 3227)
	r.Character.Job = "0"
	r.Character.Stats["grow_type"] = 1
	r.Room.mu.Lock()
	r.Room.monsters[0].Spawn.RandomDropCount = 1
	r.Room.monsters[0].Spawn.FixedDropCount = 1
	r.Room.mu.Unlock()
	return s, r, repos
}
