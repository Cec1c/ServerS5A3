package dnfbridge

import (
	"context"
	"errors"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// Keep the existing fault/count/blocking probes on the richer transaction
// used when equipment disassembly can credit account-level epic pieces.
func (t *nativeNPCAssetsTrace) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string, apply func(r.AccountRepository, r.AccountInventoryRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	t.transactions++
	return t.AccountCharacterAssetUnitOfWork.(r.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, account, character,
		func(a r.AccountRepository, w r.AccountInventoryRepository, c r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository) error {
			if err := apply(disjointFaultAccount{a, t}, nativeNPCTraceAccount{w, t}, nativeNPCTraceCharacter{c, t}, nativeNPCTraceInventory{i, t}, e); err != nil {
				return err
			}
			if t.failure == "提交" {
				return errors.New("注入提交前失败")
			}
			return nil
		})
}

type disjointFaultAccount struct {
	r.AccountRepository
	trace *nativeNPCAssetsTrace
}

func (a disjointFaultAccount) Save(ctx context.Context, record r.AccountRecord) error {
	if a.trace.failure == "账号账本" {
		return errors.New("注入账号账本保存失败")
	}
	return a.AccountRepository.Save(ctx, record)
}

func (b *nativeResultBlockingAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string, apply func(r.AccountRepository, r.AccountInventoryRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.AccountCharacterAssetUnitOfWork.(r.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, account, character, apply)
}

func (f delilahFailingInventoryAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string, apply func(r.AccountRepository, r.AccountInventoryRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	return f.inner.(r.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, account, character,
		func(a r.AccountRepository, w r.AccountInventoryRepository, c r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository) error {
			return apply(a, w, c, delilahFailInventorySave{InventoryRepository: i, saveCalls: f.saveCalls, err: f.err}, e)
		})
}

func (u countingAccountAssets) WithinAccountCharacterLedgerAssets(ctx context.Context, account, character string, apply func(r.AccountRepository, r.AccountInventoryRepository, r.CharacterRepository, r.InventoryRepository, r.EquipmentRepository) error) error {
	u.c.add("AccountAssets.Begin")
	return u.inner.(r.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, account, character,
		func(a r.AccountRepository, w r.AccountInventoryRepository, c r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository) error {
			return apply(countingAccountRepository{AccountRepository: a, c: u.c, scope: "AccountAssets.account"},
				countingAccountInventoryRepository{AccountInventoryRepository: w, c: u.c, scope: "AccountAssets.accountInventory"},
				countingCharacterRepository{CharacterRepository: c, c: u.c, scope: "AccountAssets.character"},
				countingInventoryRepository{InventoryRepository: i, c: u.c, scope: "AccountAssets.inventory"},
				countingEquipmentRepository{EquipmentRepository: e, c: u.c, scope: "AccountAssets.equipment"})
		})
}
