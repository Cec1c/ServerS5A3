package dnfbridge

import (
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) freezeCurrentGriefRewardsLocked(runtime *runtimeDungeonState) error {
	if runtime.settlementGriefRewards != nil {
		return nil
	}
	floor, err := currentGriefFloor(runtime.Dungeon)
	if err != nil || floor == 0 || runtime.clearMapCompletionKey == "" {
		return errCurrentDungeonSettlementPVFInvalid
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	candidates, err := dnfdungeon.GriefRewardCandidates(archive, uint16(floor), runtime.clearMapCompletionKey)
	if err != nil {
		return err
	}
	monsters, err := s.dungeonMonsterCatalog()
	if err != nil {
		return err
	}
	catalog, err := monsters.DropCatalog()
	if err != nil {
		return err
	}
	now := s.gameplayNow().UTC()
	resolved := &dnfdungeon.GriefRewards{}
	for i, candidate := range candidates {
		if candidate.ItemID == 0 && candidate.Count == 0 {
			continue
		}
		definition, err := catalog.ResolveItem(candidate.ItemID)
		if err != nil {
			return err
		}
		if definition.Kind != dungeonDropItemStackable {
			return errDungeonPickupItemInvalid
		}
		// The selected current-PVF rewards are stackables, so this common
		// instance initializer does not consume equipment-quality RNG draws.
		item, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(runtime.Seed), catalog, candidate.ItemID, now)
		if err != nil {
			return err
		}
		item.Count = int64(candidate.Count)
		item.Extra["source"] = "grief_settlement_reward"
		resolved.Items[i] = dnfdungeon.CardItemReward{Stack: dnfrepo.ItemStack{ItemID: item.ItemID, Count: item.Count, Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: item.Stackable, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, ExpireAt: item.ExpireAt, AllowQuickSlots: item.AllowQuickSlots, PreferQuickSlots: item.PreferQuickSlots, StackLimit: item.StackLimit}
	}
	resolved.Project = func(stack dnfrepo.ItemStack, expiration time.Time) (dnfrepo.ItemStack, error) {
		stack, _ = applyCurrentStackableExpirationAt(stack, expiration, now)
		return stack, nil
	}
	runtime.settlementGriefRewards = resolved
	return nil
}
