package dnfbridge

import (
	"fmt"
	"math"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentInventoryMovePlacement(stack dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
	if stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 {
		return dnfplacement.SlotPolicy{}, fmt.Errorf("移动物品编号无效: %d", stack.ItemID)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return dnfplacement.SlotPolicy{}, err
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		return dnfplacement.SlotPolicy{}, err
	}
	return currentPVFItemPlacementPolicy(definition), nil
}
