package dnfbridge

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Cards and ordinary monster equipment share read-only, naturally generated
// definitions. Reward gates stay with their callers; abyss and independent
// drops retain their own selection policies.
func (c *pvfDungeonDropCatalog) selectClearCardEquipment(rng *currentDungeonDropLCG, level int, down, up, rarity int64) (uint32, bool, error) {
	return c.selectGeneratedEquipment(rng, level, down, up, rarity)
}

// The legacy ItemDictionary can stop before current dungeon levels and its
// rarity aliases need not equal the item's actual rarity. Use indexed .equ
// minimum levels, rarities and creation weights for natural equipment.
func (c *pvfDungeonDropCatalog) selectGeneratedEquipment(rng *currentDungeonDropLCG, level int, down, up, rarity int64) (uint32, bool, error) {
	if c == nil || c.source == nil || rng == nil {
		return 0, false, errDungeonDropSourceRequired
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.clearCardEquipmentLoaded {
		c.clearCardEquipmentLoaded = true
		c.clearCardEquipment, c.clearCardEquipmentErr = c.loadClearCardEquipmentPool()
	}
	if c.clearCardEquipmentErr != nil {
		return 0, false, c.clearCardEquipmentErr
	}
	// 上浮为0仍可形成非空区间，例如89级引用(6,0)对应[83,89)。
	if level <= 0 || level > 200 || down < 0 || down > 200 || up < 0 || up > 200 {
		return 0, false, nil
	}
	collect := func(wanted int64) []dungeonDropWeightedItem {
		var result []dungeonDropWeightedItem
		for grade := max(int64(1), int64(level)-down); grade < min(int64(201), int64(level)+up); grade++ {
			result = append(result, c.clearCardEquipment[dungeonDropGradeRarityKey{Grade: grade, Rarity: wanted}]...)
		}
		return result
	}
	candidates := collect(rarity)
	if len(candidates) == 0 && rarity > 0 {
		candidates = collect(0)
	}
	var total uint64
	for _, item := range candidates {
		total += uint64(item.Weight)
	}
	if total == 0 {
		return 0, false, nil
	}
	if total > math.MaxUint32 {
		return 0, false, fmt.Errorf("clear-card equipment total weight exceeds RNG range: %d", total)
	}
	roll := uint64(rng.NextUint32()) % total
	for _, item := range candidates {
		if roll < uint64(item.Weight) {
			return item.ItemID, true, nil
		}
		roll -= uint64(item.Weight)
	}
	return 0, false, nil
}

// Warm the one-time archive scan before players enter dungeons. Failure is
// logged and cached; missing optional rewards do not block town startup.
func clearCardEquipmentPoolWarmUpFields(monsters *pvfDungeonMonsterCatalog) []any {
	started := time.Now()
	fields := []any{"source", "equipment definitions"}
	catalog, err := monsters.DropCatalog()
	if err != nil {
		return append(fields, "ready", false, "error", err)
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if !catalog.clearCardEquipmentLoaded {
		catalog.clearCardEquipmentLoaded = true
		catalog.clearCardEquipment, catalog.clearCardEquipmentErr = catalog.loadClearCardEquipmentPool()
	}
	count := 0
	for _, items := range catalog.clearCardEquipment {
		count += len(items)
	}
	fields = append(fields, "ready", catalog.clearCardEquipmentErr == nil, "items", count, "elapsed", time.Since(started))
	if catalog.clearCardEquipmentErr != nil {
		fields = append(fields, "error", catalog.clearCardEquipmentErr)
	}
	return fields
}

func (c *pvfDungeonDropCatalog) loadClearCardEquipmentPool() (map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem, error) {
	paths := map[string][]uint32{}
	for id, ref := range c.itemRefs {
		if ref.kind != dungeonDropItemEquipment {
			continue
		}
		path := strings.ToLower(cleanDungeonDropPath(ref.path))
		if !strings.HasPrefix(path, "equipment/") {
			path = "equipment/" + path
		}
		paths[path] = append(paths[path], id)
	}
	type candidate struct {
		key  dungeonDropGradeRarityKey
		item dungeonDropWeightedItem
	}
	selected := map[uint32]candidate{}
	originalSeen := map[uint32]bool{}
	visit := func(filePath, body string) error {
		keyPath := strings.ToLower(cleanDungeonDropPath(filePath))
		ids := paths[keyPath]
		revised := false
		if len(ids) == 0 {
			dir, base := path.Split(keyPath)
			if strings.HasPrefix(base, "(r)") {
				ids = paths[dir+strings.TrimPrefix(base, "(r)")]
				revised = true
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if !revised {
			for _, id := range ids {
				originalSeen[id] = true
				delete(selected, id)
			}
		}
		doc, err := dnfpvf.Parse(filePath, body)
		if err != nil {
			return nil
		}
		level, levelOK := doc.Int("minimum level")
		rarity, rarityOK := doc.Int("rarity")
		weight, weightOK := doc.Int("creation rate")
		if !levelOK || level <= 0 || level > 200 || !rarityOK || rarity < 0 || rarity > 6 || !weightOK || weight <= 0 || weight > math.MaxUint32 {
			return nil
		}
		if kind, ok := doc.Text("equipment type"); ok && clearCardExcludedEquipmentType(kind) {
			return nil
		}
		key := dungeonDropGradeRarityKey{Grade: level, Rarity: rarity}
		for _, id := range ids {
			if !revised || !originalSeen[id] {
				selected[id] = candidate{key: key, item: dungeonDropWeightedItem{ItemID: id, Weight: uint32(weight), MinimumLevel: level}}
			}
		}
		return nil
	}
	if scanner, ok := c.source.(hellPartyRewardTextScanner); ok {
		// Stream real archives without retaining every decoded .equ in their
		// text cache. Only small eligible item/weight rows survive the scan.
		if err := scanner.ScanTextPrefix("equipment", visit); err != nil {
			return nil, fmt.Errorf("scan clear-card equipment: %w", err)
		}
	} else {
		for path := range paths {
			actual, body, err := readDungeonDropText(c.source, "equipment", path)
			if err == nil {
				_ = visit(actual, body)
			}
		}
	}
	pool := map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem{}
	for _, row := range selected {
		pool[row.key] = append(pool[row.key], row.item)
	}
	// Archive order and map iteration must not change seeded reward selection.
	for key, items := range pool {
		sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
		pool[key] = items
	}
	return pool, nil
}

func clearCardExcludedEquipmentType(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, excluded := range []string{"avatar", "[creature]", "[title name]", "[aura", "[flag"} {
		if strings.Contains(kind, excluded) {
			return true
		}
	}
	return false
}
