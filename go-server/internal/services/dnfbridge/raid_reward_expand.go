package dnfbridge

import (
	"fmt"
	"math"
	"sort"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Only raid settlement uses this expansion. Inventory box opening and player
// selections keep their existing owners. Random wrappers are sampled with the
// same PVF parser and weighted draws as opening them, before the plan freezes.
const currentRaidRewardMaxDepth = 16
const currentRaidRewardMaxVisits = 4096

type currentRaidRewardSource struct {
	box *currentBoosterDefinition
}

type currentRaidRewardExpander struct {
	catalog *pvfDungeonDropCatalog
	sources map[uint32]currentRaidRewardSource
}

func (e *currentRaidRewardExpander) source(id uint32) (currentRaidRewardSource, error) {
	if cached, ok := e.sources[id]; ok {
		return cached, nil
	}
	definition, err := e.catalog.ResolveItem(id)
	if err != nil {
		return currentRaidRewardSource{}, err
	}
	result := currentRaidRewardSource{}
	if definition.Kind == dungeonDropItemStackable {
		switch normalizeDungeonDropStackableType(definition.StackableType) {
		case "[booster]", "[booster random]", "[cera booster]", "[upgradable legacy]", "[random upgradable legacy]":
			box, err := resolveCurrentBoosterDefinition(e.catalog, id, currentBoosterRequestRandom)
			if err != nil {
				return result, err
			}
			// A raid entitlement must not silently bypass an opening price.
			if box.MaterialItemID != 0 || box.MaterialCount != 0 || box.LotteryGoldCost != 0 {
				return result, fmt.Errorf("raid reward wrapper %d requires an opening cost", id)
			}
			result.box = &box
		}
	}
	e.sources[id] = result
	return result, nil
}

func resolveCurrentRaidRewardContents(catalog *pvfDungeonDropCatalog, sourceID uint32, roll func(int64) (int64, error), now time.Time) ([]currentBoosterReward, error) {
	if catalog == nil || catalog.source == nil || sourceID == 0 || roll == nil || now.IsZero() {
		return nil, errCurrentBoosterPVFInvalid
	}
	e := currentRaidRewardExpander{catalog: catalog, sources: map[uint32]currentRaidRewardSource{}}
	amounts := map[uint32]uint32{}
	active := map[uint32]bool{}
	visits := 0
	var expand func(uint32, uint32, int) error
	expand = func(id, count uint32, depth int) error {
		visits++
		if id == 0 || count == 0 || count > math.MaxUint16 || depth > currentRaidRewardMaxDepth || visits > currentRaidRewardMaxVisits || active[id] {
			return fmt.Errorf("raid reward expansion invalid or exceeds bounds at %d", id)
		}
		source, err := e.source(id)
		if err != nil {
			return err
		}
		if source.box == nil {
			// Materials, equipment and actual player-choice boxes are leaves.
			total := uint64(amounts[id]) + uint64(count)
			if total > math.MaxUint16 || (amounts[id] == 0 && len(amounts) >= 255) {
				return errCurrentBoosterPVFInvalid
			}
			amounts[id] = uint32(total)
			return nil
		}
		active[id] = true
		defer delete(active, id)
		// Several copies of a random box mean independent draws, never one
		// sampled result multiplied by the number of copies.
		for copy := uint32(0); copy < count; copy++ {
			children, _, err := resolveCurrentBoosterRewardAmounts(*source.box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, roll)
			if err != nil {
				return err
			}
			ids := make([]uint32, 0, len(children))
			for child := range children {
				ids = append(ids, child)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, child := range ids {
				if err := expand(child, children[child], depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := expand(sourceID, 1, 0); err != nil {
		return nil, err
	}
	return resolveCurrentBoosterRewards(catalog, amounts, nil, repo.ItemStack{}, now.UTC())
}

// Admission validates every reachable random branch, including rare children
// not selected by the zero draw. Malformed wrappers never become fallback loot.
func validateCurrentRaidRewardContents(catalog *pvfDungeonDropCatalog, roots []uint32, now time.Time) error {
	if catalog == nil || catalog.source == nil || now.IsZero() {
		return errCurrentBoosterPVFInvalid
	}
	e := currentRaidRewardExpander{catalog: catalog, sources: map[uint32]currentRaidRewardSource{}}
	active, done := map[uint32]bool{}, map[uint32]bool{}
	var visit func(uint32, int) error
	visit = func(id uint32, depth int) error {
		if id == 0 || active[id] || depth > currentRaidRewardMaxDepth {
			return fmt.Errorf("raid reward dependency cycle or invalid depth at %d", id)
		}
		if done[id] {
			return nil
		}
		if len(e.sources) >= currentRaidRewardMaxVisits {
			return errCurrentBoosterPVFInvalid
		}
		source, err := e.source(id)
		if err != nil {
			return err
		}
		active[id] = true
		defer delete(active, id)
		if source.box == nil {
			if _, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{id: 1}, nil, repo.ItemStack{}, now.UTC()); err != nil {
				return err
			}
		} else {
			if _, _, err := resolveCurrentBoosterRewardAmounts(*source.box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil }); err != nil {
				return err
			}
			for _, group := range source.box.Random.Groups {
				for _, entry := range group.Entries {
					if entry.ItemID <= 0 || entry.ItemID > math.MaxUint32 || entry.Count <= 0 || entry.Count > math.MaxUint16 {
						return errCurrentBoosterPVFInvalid
					}
					if entry.Weight > 0 {
						if err := visit(uint32(entry.ItemID), depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		done[id] = true
		return nil
	}
	for _, root := range roots {
		if err := visit(root, 0); err != nil {
			return err
		}
	}
	return nil
}
