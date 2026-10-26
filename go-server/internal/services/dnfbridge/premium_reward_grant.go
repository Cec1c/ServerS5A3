package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/premium"
)

// 奖励识别与魔盒、背包既有自动激活共用现用PVF映射；黑钻等管理专用物品不参与。
// PVF尚未装配的旧调用保留普通奖励行为，与魔盒的兼容策略一致。
func (s *Service) currentPremiumRewardContracts() map[int64]premium.InventoryContract {
	catalog, err := s.currentPremiumCatalog()
	if err != nil {
		return nil
	}
	return currentPremiumInventoryContracts(catalog)
}
