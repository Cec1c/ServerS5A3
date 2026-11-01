package inventory

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 宝珠材料、装备附魔与战令进度同一角色资产事务；穿戴装备仍用事务内装备仓储。
func (o *Owner) withinEnchantAssetTransaction(ctx context.Context, id string, apply func(*Owner) error) error {
	if o.assets == nil {
		return ErrCharacterAssetTxnRequired
	}
	return o.assets.WithinCharacterAssets(ctx, id, func(chars repo.CharacterRepository, items repo.InventoryRepository, equips repo.EquipmentRepository) error {
		owner := *o
		owner.characters = chars
		owner.repo = items
		owner.equipment = equips
		owner.inItemTransaction = true
		return apply(&owner)
	})
}
