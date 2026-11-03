package dnfbridge

import (
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentInventorySortKeys(stack r.ItemStack) (alignedcmd.InventorySortKeys, error) {
	if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
		return alignedcmd.InventorySortKeys{}, fmt.Errorf("invalid sort item %d", stack.ItemID)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return alignedcmd.InventorySortKeys{}, err
	}
	def, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return alignedcmd.InventorySortKeys{}, err
	}
	keys := alignedcmd.InventorySortKeys{MinimumLevel: def.MinimumLevel, Rarity: def.Rarity, PartRank: math.MaxInt16, StackableType: def.StackableType}
	if rule, ok := currentEquipmentPlacementRuleForPVFType(def.EquipmentType); ok {
		keys.PartRank = rule.targetSlot
	}
	return keys, nil
}
