package dnfbridge

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 观察实际事务中的账号仓库保存，不改变Load、Save或提交行为。
type extractionWarehouseWriteTrace struct {
	dnfrepo.CharacterSettlementUnitOfWork
	saves int
}

func (t *extractionWarehouseWriteTrace) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return t.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(group dnfrepo.Group) error {
		group.AccountInventory = extractionWarehouseSaveTrace{AccountInventoryRepository: group.AccountInventory, trace: t}
		return apply(group)
	})
}

type extractionWarehouseSaveTrace struct {
	dnfrepo.AccountInventoryRepository
	trace *extractionWarehouseWriteTrace
}

func (t extractionWarehouseSaveTrace) Save(ctx context.Context, record dnfrepo.AccountInventoryRecord) error {
	t.trace.saves++
	return t.AccountInventoryRepository.Save(ctx, record)
}
