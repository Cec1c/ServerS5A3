package dnfbridge

import (
	"fmt"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The reference tower rules draw five item candidates (nine on mirror floors
// 10..90), with the fixed synthesizer/clear medal resolved from the same PVF.
// Rolls are frozen before the transaction; only its receipted result is shown.
func (s *Service) freezeCurrentDespairRewardsLocked(runtime *runtimeDungeonState) error {
	if runtime.settlementDespairRewards != nil {
		return nil
	}
	floor, err := currentDespairFloor(runtime.Dungeon)
	if err != nil || floor == 0 || !runtime.Dungeon.Metadata.BasisLevel.Set {
		return errCurrentDungeonSettlementPVFInvalid
	}
	monsters, err := s.dungeonMonsterCatalog()
	if err != nil {
		return err
	}
	catalog, err := monsters.DropCatalog()
	if err != nil {
		return err
	}
	rules, err := loadCurrentDespairRewardRules(monsters.source)
	if err != nil {
		return err
	}
	seed := runtime.Seed
	var visit *runtimeDungeonRoomVisit
	if scene, ok := runtime.Session.Scene(); ok {
		if current, e := runtime.currentDungeonRoomVisit(scene); e == nil {
			visit = current
			seed = current.DropRNG
		}
	}
	rng := newCurrentDungeonDropLCG(seed)
	ids, err := currentDespairRewardCandidateIDs(floor, int(runtime.Dungeon.Metadata.BasisLevel.Value), rng, catalog, rules)
	if err != nil {
		return err
	}
	now := s.gameplayNow().UTC()
	items := make([]dnfdungeon.DespairRewardItem, 0, len(ids))
	for _, id := range ids {
		item, err := currentDungeonCardItemReward(rng, catalog, id, now)
		if err != nil {
			return fmt.Errorf("resolve despair reward %d: %w", id, err)
		}
		item.Extra["source"] = "despair_settlement_reward"
		resolved := dnfdungeon.DespairRewardItem{CardItemReward: dnfdungeon.CardItemReward{Stack: dnfrepo.ItemStack{ItemID: item.ItemID, Count: item.Count, Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: item.Stackable, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, ExpireAt: item.ExpireAt, AllowQuickSlots: item.AllowQuickSlots, PreferQuickSlots: item.PreferQuickSlots, StackLimit: item.StackLimit}}
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			return err
		}
		if definition.Kind == dungeonDropItemEquipment {
			rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType)
			if !ok || (rule.class != currentEquipmentPlacementClassAvatar && rule.class != currentEquipmentPlacementClassNormal) {
				return fmt.Errorf("despair reward item %d has unsupported equipment placement %q", id, definition.EquipmentType)
			}
		}
		if rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType); ok && rule.class == currentEquipmentPlacementClassAvatar {
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
			if err != nil {
				return err
			}
			temp := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
			slot, err := grantCurrentCeraShopAvatar(&temp, catalog.source, definition, currentCeraShopProduct{}, 0)
			if err != nil {
				return err
			}
			resolved.ListType = 1
			resolved.Stack = temp.Slots[currentCeraShopInventorySlotKey(1, slot)]
			resolved.Stack.Extra["source"] = "despair_settlement_reward"
		}
		items = append(items, resolved)
	}
	runtime.settlementDespairRewards = &dnfdungeon.DespairRewards{Items: items, Project: func(stack dnfrepo.ItemStack, expiration time.Time) (dnfrepo.ItemStack, error) {
		stack, _ = applyCurrentStackableExpirationAt(stack, expiration, now)
		return stack, nil
	}}
	if visit != nil {
		visit.DropRNG = rng.Seed()
	}
	return nil
}

func currentDespairRewardCandidateIDs(floor, level int, rng *currentDungeonDropLCG, catalog *pvfDungeonDropCatalog, rules currentDespairRewardRules) ([]uint32, error) {
	if floor < 1 || floor > 100 || level < 1 || level > 200 {
		return nil, errCurrentDungeonSettlementPVFInvalid
	}
	count := 5
	mirror := floor >= 10 && floor <= 90 && floor%10 == 0
	if mirror {
		count = 9
	}
	ids := make([]uint32, 0, 10)
	for i := 0; i < count; i++ {
		id, found, err := catalog.selectDespairEquipment(rng, rules, level)
		if err != nil {
			return nil, err
		}
		if found {
			ids = append(ids, id)
		}
	}
	if mirror {
		ids = append(ids, 1252)
	} else if floor == 100 {
		ids = append(ids, 3314)
	}
	return ids, nil
}
