package raidcurrency

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	migrationFaultInventoryCommit = "inventory-commit"
	migrationFaultAccountCommit   = "account-commit"
)

// migrationRollbackHarness stages both owners inside the callback and applies
// them only after MigrateCharacter returns. The fault is injected after the
// selected owner has been physically saved, which exercises the same rollback
// boundary as a late account/character-asset commit.
type migrationRollbackHarness struct {
	group   repo.Group
	faultAt string
	fault   error
	order   []string
}

func (h *migrationRollbackHarness) WithinAccountCharacterAssets(
	context.Context, string, string,
	func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error,
) error {
	return repo.ErrAccountCharacterAssetTransactionUnavailable
}

func (h *migrationRollbackHarness) WithinAccountCharacterLedgerAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if accountID == "" || characterID == "" || apply == nil || h == nil {
		return repo.ErrAccountCharacterAssetTransactionUnavailable
	}

	accountBefore, accountFound, err := h.group.Account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !accountFound || accountBefore.AccountID != accountID {
		return repo.ErrAccountCharacterAssetTransactionUnavailable
	}
	inventoryBefore, inventoryFound, err := h.group.Inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !inventoryFound || inventoryBefore.CharacterID != characterID {
		return repo.ErrAccountCharacterAssetTransactionUnavailable
	}

	accountTx := &migrationStagedAccount{
		AccountRepository: h.group.Account,
		accountID:         accountID,
		record:            repo.CloneAccount(accountBefore),
		exists:            true,
	}
	inventoryTx := &migrationStagedInventory{
		InventoryRepository: h.group.Inventory,
		characterID:         characterID,
		record:              repo.CloneInventory(inventoryBefore),
		exists:              true,
	}
	characterTx := &migrationScopedCharacter{
		CharacterRepository: h.group.Character,
		characterID:         characterID,
	}
	if err := apply(accountTx, h.group.AccountInventory, characterTx, inventoryTx, h.group.Equipment); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	inventoryAttempted := false
	accountAttempted := false
	rollback := func(cause error) error {
		joined := []error{cause}
		if accountAttempted {
			joined = append(joined, h.group.Account.Save(context.Background(), repo.CloneAccount(accountBefore)))
		}
		if inventoryAttempted {
			joined = append(joined, h.group.Inventory.Save(context.Background(), repo.CloneInventory(inventoryBefore)))
		}
		return errors.Join(joined...)
	}

	if inventoryTx.dirty {
		inventoryAttempted = true
		if err := h.saveInventory(ctx, inventoryTx.record); err != nil {
			return rollback(err)
		}
	}
	if accountTx.dirty {
		accountAttempted = true
		if err := h.saveAccount(ctx, accountTx.record); err != nil {
			return rollback(err)
		}
	}
	return nil
}

func (h *migrationRollbackHarness) saveInventory(ctx context.Context, record repo.InventoryRecord) error {
	if err := h.group.Inventory.Save(ctx, record); err != nil {
		return err
	}
	h.order = append(h.order, "inventory")
	if h.faultAt == migrationFaultInventoryCommit && h.fault != nil {
		return h.fault
	}
	return nil
}

func (h *migrationRollbackHarness) saveAccount(ctx context.Context, record repo.AccountRecord) error {
	if err := h.group.Account.Save(ctx, record); err != nil {
		return err
	}
	h.order = append(h.order, "account")
	if h.faultAt == migrationFaultAccountCommit && h.fault != nil {
		return h.fault
	}
	return nil
}

type migrationStagedAccount struct {
	repo.AccountRepository
	accountID string
	record    repo.AccountRecord
	exists    bool
	dirty     bool
}

func (s *migrationStagedAccount) Load(ctx context.Context, accountID string) (repo.AccountRecord, bool, error) {
	if err := repo.TransactionKeyError(ctx, s.accountID, accountID); err != nil {
		return repo.AccountRecord{}, false, err
	}
	return repo.CloneAccount(s.record), s.exists, nil
}

func (s *migrationStagedAccount) Save(ctx context.Context, record repo.AccountRecord) error {
	if err := repo.TransactionKeyError(ctx, s.accountID, record.AccountID); err != nil {
		return err
	}
	s.record = repo.CloneAccount(record)
	s.exists = true
	s.dirty = true
	return nil
}

type migrationStagedInventory struct {
	repo.InventoryRepository
	characterID string
	record      repo.InventoryRecord
	exists      bool
	dirty       bool
}

func (s *migrationStagedInventory) Load(ctx context.Context, characterID string) (repo.InventoryRecord, bool, error) {
	if err := repo.TransactionKeyError(ctx, s.characterID, characterID); err != nil {
		return repo.InventoryRecord{}, false, err
	}
	return repo.CloneInventory(s.record), s.exists, nil
}

func (s *migrationStagedInventory) Save(ctx context.Context, record repo.InventoryRecord) error {
	if err := repo.TransactionKeyError(ctx, s.characterID, record.CharacterID); err != nil {
		return err
	}
	s.record = repo.CloneInventory(record)
	s.exists = true
	s.dirty = true
	return nil
}

type migrationScopedCharacter struct {
	repo.CharacterRepository
	characterID string
}

func (s *migrationScopedCharacter) Load(ctx context.Context, characterID string) (repo.CharacterRecord, bool, error) {
	if err := repo.TransactionKeyError(ctx, s.characterID, characterID); err != nil {
		return repo.CharacterRecord{}, false, err
	}
	return s.CharacterRepository.Load(ctx, characterID)
}

func TestMigrateCharacterFinalCommitFailureDoesNotPartiallyConsolidate(t *testing.T) {
	tests := []struct {
		name      string
		faultAt   string
		wantOrder []string
	}{
		{name: "inventory", faultAt: migrationFaultInventoryCommit, wantOrder: []string{"inventory"}},
		{name: "account", faultAt: migrationFaultAccountCommit, wantOrder: []string{"inventory", "account"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.FixedZone("CST", 8*60*60))
			group := memory.NewMemoryGroup()
			seed := func() {
				if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: "account-a"}); err != nil {
					t.Fatal(err)
				}
				if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "character-1", AccountID: "account-a"}); err != nil {
					t.Fatal(err)
				}
				if err := group.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "character-1", Slots: map[string]repo.ItemStack{
					"0:7": {ItemID: int64(FateItemID), Count: 3},
					"0:8": {ItemID: 42, Count: 9},
				}}); err != nil {
					t.Fatal(err)
				}
			}
			seed()
			accountBefore, accountFound, err := group.Account.Load(ctx, "account-a")
			if err != nil || !accountFound {
				t.Fatalf("load account: found=%t err=%v", accountFound, err)
			}
			inventoryBefore, inventoryFound, err := group.Inventory.Load(ctx, "character-1")
			if err != nil || !inventoryFound {
				t.Fatalf("load inventory: found=%t err=%v", inventoryFound, err)
			}

			fault := errors.New("injected final " + tc.name + " commit failure")
			harness := &migrationRollbackHarness{group: group, faultAt: tc.faultAt, fault: fault}
			group.AccountAssets = harness
			moved, err := MigrateCharacter(ctx, group, "account-a", "character-1", now)
			if moved != 0 || !errors.Is(err, fault) {
				t.Fatalf("MigrateCharacter moved=%d err=%v, want moved=0 fault=%v", moved, err, fault)
			}
			if !reflect.DeepEqual(harness.order, tc.wantOrder) {
				t.Fatalf("commit order=%v, want %v", harness.order, tc.wantOrder)
			}

			accountAfter, accountFound, err := group.Account.Load(ctx, "account-a")
			if err != nil || !accountFound || !reflect.DeepEqual(accountAfter, accountBefore) {
				t.Fatalf("account changed after failed commit: before=%+v after=%+v found=%t err=%v", accountBefore, accountAfter, accountFound, err)
			}
			inventoryAfter, inventoryFound, err := group.Inventory.Load(ctx, "character-1")
			if err != nil || !inventoryFound || !reflect.DeepEqual(inventoryAfter, inventoryBefore) {
				t.Fatalf("inventory changed after failed commit: before=%+v after=%+v found=%t err=%v", inventoryBefore, inventoryAfter, inventoryFound, err)
			}

			group.AccountAssets = &migrationRollbackHarness{group: group}
			moved, err = MigrateCharacter(ctx, group, "account-a", "character-1", now)
			if err != nil || moved != 3 {
				t.Fatalf("retry moved=%d err=%v, want 3", moved, err)
			}
			accountAfter, _, err = group.Account.Load(ctx, "account-a")
			if err != nil {
				t.Fatal(err)
			}
			state, err := Read(accountAfter, now)
			if err != nil || state.Balance != 3 || state.Earned != 0 {
				t.Fatalf("retry state=%+v err=%v", state, err)
			}
			inventoryAfter, _, err = group.Inventory.Load(ctx, "character-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, found := inventoryAfter.Slots["0:7"]; found {
				t.Fatalf("fate source survived successful retry: %+v", inventoryAfter.Slots)
			}
			if got := inventoryAfter.Slots["0:8"]; got.ItemID != 42 || got.Count != 9 {
				t.Fatalf("ordinary item changed: %+v", got)
			}
		})
	}
}
