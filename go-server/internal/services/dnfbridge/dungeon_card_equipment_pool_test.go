package dnfbridge

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type clearCardScanSource struct {
	dungeonDropCatalogTestSource
	order     []string
	scanError error
	scans     *int
}

func (s clearCardScanSource) ScanTextPrefix(_ string, visit func(string, string) error) error {
	if s.scans != nil {
		*s.scans++
	}
	for _, p := range s.order {
		if err := visit(p, s.dungeonDropCatalogTestSource[p]); err != nil {
			return err
		}
	}
	return s.scanError
}

func TestClearCardStreamingRevisedPathsPreserveOriginalPriority(t *testing.T) {
	for _, original := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			source := clearCardEquipmentTestSource()
			originalPath, revisedPath := "equipment/weapon/test.equ", "equipment/weapon/(r)test.equ"
			source[revisedPath] = source[originalPath]
			order := []string{revisedPath}
			if original {
				// Existing zero-rate originals must not be overridden by a
				// positive-rate revised sibling, irrespective of archive order.
				source[originalPath] = strings.ReplaceAll(source[originalPath], "[creation rate] 10", "[creation rate] 0")
				if reverse {
					order = append(order, originalPath)
				} else {
					order = append([]string{originalPath}, order...)
				}
			} else {
				delete(source, originalPath)
			}
			catalog, err := newPVFDungeonDropCatalog(clearCardScanSource{dungeonDropCatalogTestSource: source, order: order})
			if err != nil {
				t.Fatal(err)
			}
			id, found, err := catalog.selectClearCardEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
			if err != nil || found == original || (found && id != 1001) {
				t.Fatalf("original=%t reverse=%t id=%d found=%t err=%v", original, reverse, id, found, err)
			}
		}
	}
}

func TestClearCardPartialScanCannotPublishPartialRewards(t *testing.T) {
	source := clearCardScanSource{dungeonDropCatalogTestSource: clearCardEquipmentTestSource(), order: []string{"equipment/weapon/test.equ"}, scanError: errors.New("archive read failed")}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, found, err := catalog.selectClearCardEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
		if !errors.Is(err, source.scanError) || found {
			t.Fatalf("partial pool published: found=%t err=%v", found, err)
		}
	}
}

func TestClearCardUsesPVFWeightsAndLevelWindow(t *testing.T) {
	source := clearCardEquipmentTestSource()
	source["equipment/equipment.lst"] += "\n1004 `weapon/heavy.equ`"
	source["equipment/weapon/heavy.equ"] = strings.ReplaceAll(source["equipment/weapon/test.equ"], "[creation rate] 10", "[creation rate] 30")
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[uint32]int{}
	for seed := uint32(1); seed <= 1000; seed++ {
		id, found, err := catalog.selectClearCardEquipment(newCurrentDungeonDropLCG(seed*1234567), 80, 0, 1, 2)
		if err != nil || !found {
			t.Fatalf("weighted selection failed: %t %v", found, err)
		}
		counts[id]++
	}
	if len(counts) != 2 || counts[1004] < 2*counts[1001] || counts[1004] > 4*counts[1001] {
		t.Fatalf("PVF 1:3 weights not reflected: %v", counts)
	}
	for _, level := range []int{79, 81, 86} {
		_, found, err := catalog.selectClearCardEquipment(newCurrentDungeonDropLCG(1), level, 0, 1, 2)
		if err != nil || found {
			t.Fatalf("level=%d escaped minimum-level window: %t %v", level, found, err)
		}
	}
}

func clearCardEquipmentTestSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":                "1 `test.mob`",
		"equipment/equipment.lst":            "1001 `weapon/test.equ`\n1002 `weapon/disabled.equ`\n1003 `avatar/test.equ`",
		"stackable/stackable.lst":            "2001 `material/test_recipe.stk`",
		"equipment/weapon/test.equ":          "[equipment type] `[weapon]` 1\n[minimum level] 80\n[grade] 86\n[rarity] 2\n[creation rate] 10\n[durability] 40",
		"equipment/weapon/disabled.equ":      "[equipment type] `[weapon]` 1\n[minimum level] 80\n[grade] 86\n[rarity] 2\n[creation rate] 0",
		"equipment/avatar/test.equ":          "[equipment type] `[coat avatar]` 1\n[minimum level] 80\n[grade] 86\n[rarity] 2\n[creation rate] 1000",
		"stackable/material/test_recipe.stk": "[stackable type] `[recipe]`\n[grade] 80\n[rarity] 2\n[creation rate] 10",
		dungeonDropEquipmentDictionaryPath:   "", // Older dictionary omits high-level equipment.
	}
}

func TestClearCardUsesEquipmentDefinitionsWhenDictionaryIsIncomplete(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(clearCardEquipmentTestSource())
	if err != nil {
		t.Fatal(err)
	}
	foundEquipment := 0
	for seed := uint32(1); seed <= 100; seed++ {
		item, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(seed*1234567), catalog, 80, dungeonCardPVFItemDropReference{ValueA: 0, ValueB: 1}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			continue
		}
		foundEquipment++
		if item.ItemID != 1001 || item.Stackable || item.Extra["quality_seed"] == "" {
			t.Fatalf("ordinary card selected a recipe, disabled item or avatar: %+v", item)
		}
	}
	if foundEquipment == 0 {
		t.Fatal("high-level equipment from its own PVF definition was never selected")
	}
}

func TestClearCardDoesNotSubstituteRecipesForMissingEquipment(t *testing.T) {
	source := clearCardEquipmentTestSource()
	source["equipment/equipment.lst"] = "1002 `weapon/disabled.equ`"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for seed := uint32(1); seed <= 100; seed++ {
		_, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(seed*1234567), catalog, 80, dungeonCardPVFItemDropReference{ValueB: 1}, time.Now())
		if err != nil || found {
			t.Fatalf("empty equipment pool: found=%t err=%v", found, err)
		}
	}
}

func TestRealPVFClearCardsHaveHighLevelEquipment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	document, err := parseDungeonCardPVFDocument(archive, dungeonCardDropRulePath)
	if err != nil {
		t.Fatal(err)
	}
	references, err := parseDungeonCardPVFItemDropReferences(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{20, 80, 90} {
		seen := map[int64]bool{}
		for seed := uint32(1); seed <= 128; seed++ {
			item, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(seed*1234567), catalog, level, references[int64(level)], time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if !found {
				continue
			}
			if item.Stackable || item.Extra["item_kind"] != "equipment" {
				t.Fatalf("level=%d non-equipment=%+v", level, item)
			}
			seen[item.ItemID] = true
		}
		t.Logf("level=%d distinct equipment=%d", level, len(seen))
		if len(seen) < 2 {
			t.Fatalf("level=%d equipment pool missing or fixed to one item", level)
		}
	}
}

func TestClearCardStartupWarmUpReusesPoolForRewards(t *testing.T) {
	scans := 0
	source := clearCardScanSource{dungeonDropCatalogTestSource: clearCardEquipmentTestSource(), order: []string{"equipment/weapon/test.equ"}, scans: &scans}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	fields := clearCardEquipmentPoolWarmUpFields(monsters)
	if scans != 1 {
		t.Fatalf("startup did not scan exactly once: %d fields=%v", scans, fields)
	}
	catalog, err := monsters.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	id, found, err := catalog.selectClearCardEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
	if err != nil || !found || id != 1001 {
		t.Fatalf("warmed reward unavailable: %d %t %v", id, found, err)
	}
	clearCardEquipmentPoolWarmUpFields(monsters)
	if scans != 1 {
		t.Fatalf("reward or repeated warmup rescanned archive: %d", scans)
	}
}
