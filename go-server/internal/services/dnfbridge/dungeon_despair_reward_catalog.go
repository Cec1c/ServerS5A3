package dnfbridge

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const despairClearRewardPath = "etc/itemdropinfo_clearreward.etc"

type currentDespairRewardRules struct {
	types  []int64
	rarity []int64
	ranges map[int64]dungeonCardPVFItemDropReference
}

func loadCurrentDespairRewardRules(source dnfpvf.Source) (currentDespairRewardRules, error) {
	doc, err := parseDungeonCardPVFDocument(source, despairClearRewardPath)
	if err != nil {
		return currentDespairRewardRules{}, err
	}
	types, err := dungeonCardPVFSectionInts(doc, "drop item type prob")
	if err != nil {
		return currentDespairRewardRules{}, err
	}
	if len(types) != 4 {
		return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
	}
	var total int64
	for i, v := range types {
		if v < 0 || v > math.MaxInt32 || (v != 0 && i != 1 && i != 3) {
			return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
		}
		total += v
	}
	if total <= 0 || total > math.MaxInt32 {
		return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
	}
	rarity, err := dungeonCardPVFSectionInts(doc, "basis of rarity dicision")
	if err != nil {
		return currentDespairRewardRules{}, err
	}
	if len(rarity) < 1 || len(rarity) > 7 {
		return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
	}
	for i, v := range rarity {
		if v < 0 || v > math.MaxInt32 || (i > 0 && v < rarity[i-1]) {
			return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
		}
	}
	if rarity[len(rarity)-1] < 1000000 {
		return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
	}
	ranges, err := parseDungeonCardPVFItemDropReferences(doc)
	if err != nil {
		return currentDespairRewardRules{}, err
	}
	for key, row := range ranges {
		if key < 1 || key > 200 || row.ValueA < 0 || row.ValueA > 200 || row.ValueB < 1 || row.ValueB > 200 {
			return currentDespairRewardRules{}, errDungeonCardPVFSectionShape
		}
	}
	return currentDespairRewardRules{types: types, rarity: rarity, ranges: ranges}, nil
}

// Tower candidates use [grade], including a distinct avatar pool, as in the
// reference GenerateItemCard. Ordinary cards' minimum-level pool stays separate.
func (c *pvfDungeonDropCatalog) loadDespairEquipmentPools() ([2]map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem, error) {
	var pools [2]map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem
	for i := range pools {
		pools[i] = make(map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem)
	}
	paths := map[string][]uint32{}
	for id, ref := range c.itemRefs {
		if ref.kind == dungeonDropItemEquipment {
			p := strings.ToLower(cleanDungeonDropPath(ref.path))
			if !strings.HasPrefix(p, "equipment/") {
				p = "equipment/" + p
			}
			paths[p] = append(paths[p], id)
		}
	}
	type candidate struct {
		kind int
		key  dungeonDropGradeRarityKey
		item dungeonDropWeightedItem
	}
	selected := map[uint32]candidate{}
	original := map[uint32]bool{}
	visit := func(filePath, body string) error {
		p := strings.ToLower(cleanDungeonDropPath(filePath))
		ids := paths[p]
		revised := false
		if len(ids) == 0 {
			dir, base := path.Split(p)
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
				original[id] = true
				delete(selected, id)
			}
		}
		doc, err := dnfpvf.Parse(filePath, body)
		if err != nil {
			return nil
		}
		grade, gok := doc.Int("grade")
		rarity, rok := doc.Int("rarity")
		weight, wok := doc.Int("creation rate")
		if !gok || grade < 1 || grade > 200 || !rok || rarity < 0 || rarity > 6 || !wok || weight <= 0 || weight > math.MaxInt32 {
			return nil
		}
		kind, _ := doc.Text("equipment type")
		kind = strings.ToLower(kind)
		avatar := strings.Contains(p, "/avatar/") || strings.Contains(p, "/at_avatar/") || strings.Contains(kind, "avatar")
		bucket := 0
		if avatar {
			bucket = 1
		}
		for _, id := range ids {
			if !revised || !original[id] {
				selected[id] = candidate{kind: bucket, key: dungeonDropGradeRarityKey{Grade: grade, Rarity: rarity}, item: dungeonDropWeightedItem{ItemID: id, Weight: uint32(weight)}}
			}
		}
		return nil
	}
	if scanner, ok := c.source.(hellPartyRewardTextScanner); ok {
		if err := scanner.ScanTextPrefix("equipment", visit); err != nil {
			return pools, err
		}
	} else {
		for p := range paths {
			actual, body, err := readDungeonDropText(c.source, "equipment", p)
			if err == nil {
				_ = visit(actual, body)
			}
		}
	}
	for _, row := range selected {
		pools[row.kind][row.key] = append(pools[row.kind][row.key], row.item)
	}
	for _, pool := range pools {
		for key, items := range pool {
			sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
			pool[key] = items
		}
	}
	return pools, nil
}

func (c *pvfDungeonDropCatalog) selectDespairEquipment(rng *currentDungeonDropLCG, rules currentDespairRewardRules, level int) (uint32, bool, error) {
	if c == nil || c.source == nil {
		return 0, false, errDungeonDropSourceRequired
	}
	var totalTypes int64
	for _, v := range rules.types {
		totalTypes += v
	}
	if rng == nil || totalTypes <= 0 {
		return 0, false, errDungeonCardPVFSectionShape
	}
	roll := int64(rng.Next(int(totalTypes)))
	kind := 0
	for i, v := range rules.types {
		if roll < v {
			kind = i + 1
			break
		}
		roll -= v
	}
	rarityRoll := int64(rng.Next(1000000) + 1)
	rarity := int64(0)
	for i, v := range rules.rarity {
		if rarityRoll <= v {
			rarity = int64(i)
			break
		}
	}
	if kind != 2 && kind != 4 {
		return 0, false, errDungeonCardPVFSectionShape
	}
	key := int64(0)
	var reference dungeonCardPVFItemDropReference
	for k, v := range rules.ranges {
		if k <= int64(level) && k > key {
			key = k
			reference = v
		}
	}
	if key == 0 {
		return 0, false, errDungeonCardPVFSectionMissing
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.despairPoolLoaded {
		c.despairPoolLoaded = true
		c.despairPools, c.despairPoolErr = c.loadDespairEquipmentPools()
	}
	if c.despairPoolErr != nil {
		return 0, false, c.despairPoolErr
	}
	bucket := 0
	if kind == 4 {
		bucket = 1
	}
	var candidates []dungeonDropWeightedItem
	var weight uint64
	for grade := max(int64(1), int64(level)-reference.ValueA); grade < min(int64(201), int64(level)+reference.ValueB); grade++ {
		for _, item := range c.despairPools[bucket][dungeonDropGradeRarityKey{Grade: grade, Rarity: rarity}] {
			candidates = append(candidates, item)
			weight += uint64(item.Weight)
		}
	}
	if weight == 0 {
		return 0, false, nil
	}
	if weight > math.MaxInt32 {
		return 0, false, fmt.Errorf("despair equipment weight exceeds RNG range: %d", weight)
	}
	choice := uint64(rng.Next(int(weight)))
	for _, item := range candidates {
		if choice < uint64(item.Weight) {
			return item.ItemID, true, nil
		}
		choice -= uint64(item.Weight)
	}
	return 0, false, errDungeonCardPVFSectionShape
}
