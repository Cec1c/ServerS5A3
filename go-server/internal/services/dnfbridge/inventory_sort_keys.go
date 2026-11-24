package dnfbridge

import (
	"errors"
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// currentInventorySortKeys 把一件道具解析成客户端整理用的排序键。
//
// ★ 单件道具解析失败**绝不能**让整次整理失败：客户端收不到整理回包会把
// 物品栏锁死，界面表现就是「正在使用物品栏」，连分解时装都点不动
// （2026-11-08 角色 PPP 现场：时装背包里 7 件礼包时装的道具号在当前 PVF
// 里查不到，整个时装背包既不能整理也不能分解）。
// 因此「PVF 里查不到这件道具」退化成中性排序键 + 一条诊断日志，
// 让它按 item id 兜底排到最后；其余道具照常整理。
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
		if !errors.Is(err, errDungeonDropItemUnresolved) {
			// PVF 整体不可用之类属于全局故障，仍然 fail-close。
			return alignedcmd.InventorySortKeys{}, err
		}
		s.logPacketEvent("game-inventory-sort-item-unresolved",
			"item_id", stack.ItemID,
			"fallback", "neutral_sort_key",
			"error", err.Error())
		return alignedcmd.InventorySortKeys{PartRank: math.MaxInt16}, nil
	}
	keys := alignedcmd.InventorySortKeys{MinimumLevel: def.MinimumLevel, Rarity: def.Rarity, PartRank: math.MaxInt16, StackableType: def.StackableType}
	if rule, ok := currentEquipmentPlacementRuleForPVFType(def.EquipmentType); ok {
		keys.PartRank = rule.targetSlot
	}
	return keys, nil
}
