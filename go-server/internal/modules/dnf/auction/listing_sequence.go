package auction

import (
	"context"
	"math"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 拍卖编号随托管账本持久化，重启和多个 owner 不能复用旧窗口里的编号。
// 失败可以留下编号空隙，不能先取走物品再补记编号。
func (o *Owner) AllocateListingID(ctx context.Context) (uint64, error) {
	if o == nil || o.repositories.CharacterAssets == nil {
		return 0, ErrUnavailable
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return 0, err
	}
	var id uint64
	err := o.repositories.CharacterAssets.WithinCharacterAssets(ctx, EscrowCharacterID, func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		record, found, err := c.Load(ctx, EscrowCharacterID)
		if err != nil {
			return err
		}
		if !found || record.AccountID != EscrowAccountID || record.Stats == nil {
			return ErrUnavailable
		}
		next := record.Stats["auction_listing_sequence"]
		if next == 0 {
			next = o.clock().Unix() * 1_000_000
		}
		if next <= 0 || next == math.MaxInt64 {
			return ErrInvalidRequest
		}
		next++
		record.Stats["auction_listing_sequence"] = next
		record.UpdatedAt = o.clock()
		if err := repo.SaveCharacterFields(ctx, c, record, repo.CharacterFieldStats); err != nil {
			return err
		}
		id = uint64(next)
		return nil
	})
	return id, err
}
