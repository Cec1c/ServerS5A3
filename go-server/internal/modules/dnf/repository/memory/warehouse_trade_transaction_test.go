package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
)

// 本文件只覆盖 WithinCharacterWarehouseTrade 的事务边界：两个账号仓库与两个角色的
// 钱包/背包是否同提交、失败是否整批回滚、事务外 owner 是否被拒绝。可交易物品、
// 账号仓库槽位归属等领域规则不在此重复验证。

const (
	warehouseTradeCharacterA       = "19"
	warehouseTradeCharacterB       = "20"
	warehouseTradeAccountA         = "acc-19"
	warehouseTradeAccountB         = "acc-20"
	warehouseTradeForeignCharacter = "21"
	warehouseTradeForeignAccount   = "acc-21"
)

var (
	warehouseTradeCharacters = []string{warehouseTradeCharacterA, warehouseTradeCharacterB}
	warehouseTradeAccounts   = []string{warehouseTradeAccountA, warehouseTradeAccountB}

	errWarehouseTradeAccountStoreFailure = errors.New("warehouse trade account store failed")
)

// warehouseTradeAccountStoreFault 在目标账号第一次 Save 落库之后注入失败，用来确认
// 同一事务中更早提交的账号仓库会随失败回滚。
type warehouseTradeAccountStoreFault struct {
	repository.AccountInventoryRepository
	failID string
	failed bool
	saved  []string
}

func (s *warehouseTradeAccountStoreFault) Save(ctx context.Context, record repository.AccountInventoryRecord) error {
	if err := s.AccountInventoryRepository.Save(ctx, record); err != nil {
		return err
	}
	s.saved = append(s.saved, record.AccountID)
	if !s.failed && record.AccountID == s.failID {
		s.failed = true
		return errWarehouseTradeAccountStoreFailure
	}
	return nil
}

type warehouseTradeOwnerState struct {
	characters  map[string]repository.CharacterRecord
	inventories map[string]repository.InventoryRecord
	accounts    map[string]repository.AccountInventoryRecord
}

func (s warehouseTradeOwnerState) requireEqual(t *testing.T, other warehouseTradeOwnerState, label string) {
	t.Helper()
	if !reflect.DeepEqual(s, other) {
		t.Fatalf("%s: owner state changed\nbefore=%+v\nafter=%+v", label, s, other)
	}
}

func warehouseTradeUnit(repos repository.Group) *memoryCharacterTradeUnitOfWork {
	return repos.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
}

func seedWarehouseTradeOwners(t *testing.T, ctx context.Context, repos repository.Group) {
	t.Helper()
	for _, record := range []repository.CharacterRecord{
		{CharacterID: warehouseTradeCharacterA, AccountID: warehouseTradeAccountA, Level: 90, Stats: map[string]int64{"gold": 1000, "fatigue": 41}},
		{CharacterID: warehouseTradeCharacterB, AccountID: warehouseTradeAccountB, Level: 90, Stats: map[string]int64{"gold": 500, "fatigue": 42}},
	} {
		if err := repos.Character.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []repository.InventoryRecord{
		{CharacterID: warehouseTradeCharacterA, Slots: map[string]repository.ItemStack{"0:10": {ItemID: 100300212, Count: 1}}},
		{CharacterID: warehouseTradeCharacterB, Slots: map[string]repository.ItemStack{"0:11": {ItemID: 100300213, Count: 2}}},
	} {
		if err := repos.Inventory.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []repository.AccountInventoryRecord{
		{AccountID: warehouseTradeAccountA, Slots: map[string]repository.ItemStack{repository.AccountSharedInventorySlotKey(354): {ItemID: 3033, Count: 3}}},
		{AccountID: warehouseTradeAccountB, Slots: map[string]repository.ItemStack{repository.AccountSharedInventorySlotKey(355): {ItemID: 3034, Count: 4}}},
	} {
		if err := repos.AccountInventory.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
}

func warehouseTradeOwnerSnapshot(t *testing.T, ctx context.Context, repos repository.Group) warehouseTradeOwnerState {
	t.Helper()
	state := warehouseTradeOwnerState{
		characters:  map[string]repository.CharacterRecord{},
		inventories: map[string]repository.InventoryRecord{},
		accounts:    map[string]repository.AccountInventoryRecord{},
	}
	for _, id := range warehouseTradeCharacters {
		character, found, err := repos.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("character %s missing", id)
		}
		state.characters[id] = character
		inventory, found, err := repos.Inventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("inventory %s missing", id)
		}
		state.inventories[id] = inventory
	}
	for _, id := range warehouseTradeAccounts {
		account, found, err := repos.AccountInventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("account warehouse %s missing", id)
		}
		state.accounts[id] = account
	}
	return state
}

// stageWarehouseTradeMutation 在两个角色钱包、两个背包和一个账号仓库槽位上都留下改动，
// 作为"要么全部提交、要么全部回滚"的观测点。
func stageWarehouseTradeMutation(
	ctx context.Context,
	chars repository.CharacterRepository,
	bags repository.InventoryRepository,
	warehouse repository.AccountInventoryRepository,
) error {
	for _, id := range warehouseTradeCharacters {
		character, found, err := chars.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("character missing inside the warehouse trade")
		}
		if id == warehouseTradeCharacterA {
			character.Stats["gold"] -= 100
		} else {
			character.Stats["gold"] += 100
		}
		if err := chars.Save(ctx, character); err != nil {
			return err
		}
	}
	source, found, err := bags.Load(ctx, warehouseTradeCharacterA)
	if err != nil {
		return err
	}
	stack, ok := source.Slots["0:10"]
	if !found || !ok {
		return errors.New("source inventory slot missing inside the warehouse trade")
	}
	delete(source.Slots, "0:10")
	if err := bags.Save(ctx, source); err != nil {
		return err
	}
	target, _, err := bags.Load(ctx, warehouseTradeCharacterB)
	if err != nil {
		return err
	}
	target.Slots["0:20"] = stack
	if err := bags.Save(ctx, target); err != nil {
		return err
	}
	sourceAccount, _, err := warehouse.Load(ctx, warehouseTradeAccountA)
	if err != nil {
		return err
	}
	shared, ok := sourceAccount.Slots[repository.AccountSharedInventorySlotKey(354)]
	if !ok {
		return errors.New("source warehouse slot missing inside the warehouse trade")
	}
	delete(sourceAccount.Slots, repository.AccountSharedInventorySlotKey(354))
	if err := warehouse.Save(ctx, sourceAccount); err != nil {
		return err
	}
	targetAccount, _, err := warehouse.Load(ctx, warehouseTradeAccountB)
	if err != nil {
		return err
	}
	targetAccount.Slots[repository.AccountSharedInventorySlotKey(356)] = shared
	return warehouse.Save(ctx, targetAccount)
}

// firstWarehouseTradeRejection 返回第一个非 nil 的越权错误；全部成功时返回显式失败，
// 避免越权用例在没有任何拒绝的情况下静默通过。
func firstWarehouseTradeRejection(candidates ...error) error {
	for _, err := range candidates {
		if err != nil {
			return err
		}
	}
	return errors.New("out-of-scope owners were reachable inside the warehouse trade")
}

func TestWarehouseTradeCommitsTwoAccountWarehousesWithTwoCharacterWallets(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedWarehouseTradeOwners(t, ctx, repos)

	err := warehouseTradeUnit(repos).WithinCharacterWarehouseTrade(ctx, warehouseTradeCharacters, warehouseTradeAccounts,
		func(chars repository.CharacterRepository, bags repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
			return stageWarehouseTradeMutation(ctx, chars, bags, warehouse)
		})
	if err != nil {
		t.Fatal(err)
	}

	characterA, _, _ := repos.Character.Load(ctx, warehouseTradeCharacterA)
	characterB, _, _ := repos.Character.Load(ctx, warehouseTradeCharacterB)
	if characterA.Stats["gold"] != 900 || characterB.Stats["gold"] != 600 {
		t.Fatalf("gold = %d/%d, want 900/600", characterA.Stats["gold"], characterB.Stats["gold"])
	}
	if characterA.Stats["fatigue"] != 41 || characterB.Stats["fatigue"] != 42 {
		t.Fatalf("unrelated stats changed: %+v %+v", characterA.Stats, characterB.Stats)
	}
	bagA, _, _ := repos.Inventory.Load(ctx, warehouseTradeCharacterA)
	bagB, _, _ := repos.Inventory.Load(ctx, warehouseTradeCharacterB)
	if _, ok := bagA.Slots["0:10"]; ok {
		t.Fatalf("source bag kept the traded stack: %+v", bagA.Slots)
	}
	if stack := bagB.Slots["0:20"]; stack.ItemID != 100300212 || stack.Count != 1 {
		t.Fatalf("target bag stack = %+v", stack)
	}
	if stack := bagB.Slots["0:11"]; stack.ItemID != 100300213 || stack.Count != 2 {
		t.Fatalf("target bag lost its own stack: %+v", bagB.Slots)
	}
	warehouseA, _, _ := repos.AccountInventory.Load(ctx, warehouseTradeAccountA)
	warehouseB, _, _ := repos.AccountInventory.Load(ctx, warehouseTradeAccountB)
	if _, ok := warehouseA.Slots[repository.AccountSharedInventorySlotKey(354)]; ok {
		t.Fatalf("source warehouse kept the traded slot: %+v", warehouseA.Slots)
	}
	if stack := warehouseB.Slots[repository.AccountSharedInventorySlotKey(356)]; stack.ItemID != 3033 || stack.Count != 3 {
		t.Fatalf("target warehouse stack = %+v", stack)
	}
	if stack := warehouseB.Slots[repository.AccountSharedInventorySlotKey(355)]; stack.ItemID != 3034 || stack.Count != 4 {
		t.Fatalf("target warehouse lost its own slot: %+v", warehouseB.Slots)
	}
}

func TestWarehouseTradeCallbackErrorLeavesEveryOwnerUntouched(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedWarehouseTradeOwners(t, ctx, repos)
	before := warehouseTradeOwnerSnapshot(t, ctx, repos)
	wantErr := errors.New("reject warehouse trade after saving")
	staged := false

	err := warehouseTradeUnit(repos).WithinCharacterWarehouseTrade(ctx, warehouseTradeCharacters, warehouseTradeAccounts,
		func(chars repository.CharacterRepository, bags repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
			if err := stageWarehouseTradeMutation(ctx, chars, bags, warehouse); err != nil {
				return err
			}
			staged = true
			return wantErr
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !staged {
		t.Fatal("callback did not stage any mutation, the rollback check would be vacuous")
	}
	before.requireEqual(t, warehouseTradeOwnerSnapshot(t, ctx, repos), "callback error")
}

func TestWarehouseTradeRejectsForeignAccountAndCharacterWithoutChanges(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedWarehouseTradeOwners(t, ctx, repos)
	before := warehouseTradeOwnerSnapshot(t, ctx, repos)
	var (
		characterLoadErr, characterSaveErr error
		inventoryLoadErr, inventorySaveErr error
		accountLoadErr, accountSaveErr     error
	)

	err := warehouseTradeUnit(repos).WithinCharacterWarehouseTrade(ctx, warehouseTradeCharacters, warehouseTradeAccounts,
		func(chars repository.CharacterRepository, bags repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
			if _, _, characterLoadErr = chars.Load(ctx, warehouseTradeForeignCharacter); characterLoadErr == nil {
				t.Error("foreign character load unexpectedly succeeded")
			}
			characterSaveErr = chars.Save(ctx, repository.CharacterRecord{CharacterID: warehouseTradeForeignCharacter, Stats: map[string]int64{"gold": 1}})
			if characterSaveErr == nil {
				t.Error("foreign character save unexpectedly succeeded")
			}
			if _, _, inventoryLoadErr = bags.Load(ctx, warehouseTradeForeignCharacter); inventoryLoadErr == nil {
				t.Error("foreign inventory load unexpectedly succeeded")
			}
			inventorySaveErr = bags.Save(ctx, repository.InventoryRecord{CharacterID: warehouseTradeForeignCharacter})
			if inventorySaveErr == nil {
				t.Error("foreign inventory save unexpectedly succeeded")
			}
			if _, _, accountLoadErr = warehouse.Load(ctx, warehouseTradeForeignAccount); accountLoadErr == nil {
				t.Error("foreign account warehouse load unexpectedly succeeded")
			}
			accountSaveErr = warehouse.Save(ctx, repository.AccountInventoryRecord{AccountID: warehouseTradeForeignAccount})
			if accountSaveErr == nil {
				t.Error("foreign account warehouse save unexpectedly succeeded")
			}
			if err := stageWarehouseTradeMutation(ctx, chars, bags, warehouse); err != nil {
				return err
			}
			return firstWarehouseTradeRejection(
				characterLoadErr, characterSaveErr,
				inventoryLoadErr, inventorySaveErr,
				accountLoadErr, accountSaveErr,
			)
		})
	if !errors.Is(err, platformdb.ErrRecordKeyRequired) {
		t.Fatalf("transaction error = %v, want %v", err, platformdb.ErrRecordKeyRequired)
	}
	if !errors.Is(characterLoadErr, platformdb.ErrRecordKeyRequired) || !errors.Is(characterSaveErr, platformdb.ErrRecordKeyRequired) {
		t.Fatalf("character rejection = %v/%v, want %v", characterLoadErr, characterSaveErr, platformdb.ErrRecordKeyRequired)
	}
	if !errors.Is(inventoryLoadErr, platformdb.ErrRecordKeyRequired) || !errors.Is(inventorySaveErr, platformdb.ErrRecordKeyRequired) {
		t.Fatalf("inventory rejection = %v/%v, want %v", inventoryLoadErr, inventorySaveErr, platformdb.ErrRecordKeyRequired)
	}
	if !errors.Is(accountLoadErr, repository.ErrCharacterTradeTransactionUnavailable) ||
		!errors.Is(accountSaveErr, repository.ErrCharacterTradeTransactionUnavailable) {
		t.Fatalf("account rejection = %v/%v, want %v", accountLoadErr, accountSaveErr, repository.ErrCharacterTradeTransactionUnavailable)
	}
	before.requireEqual(t, warehouseTradeOwnerSnapshot(t, ctx, repos), "out-of-scope owner access")
}

func TestWarehouseTradeRollsBackFirstAccountWhenSecondAccountStoreFails(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedWarehouseTradeOwners(t, ctx, repos)
	before := warehouseTradeOwnerSnapshot(t, ctx, repos)

	unit := warehouseTradeUnit(repos)
	fault := &warehouseTradeAccountStoreFault{AccountInventoryRepository: repos.AccountInventory, failID: warehouseTradeAccountB}
	unit.accountInventory = fault
	err := unit.WithinCharacterWarehouseTrade(ctx, warehouseTradeCharacters, warehouseTradeAccounts,
		func(chars repository.CharacterRepository, bags repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
			return stageWarehouseTradeMutation(ctx, chars, bags, warehouse)
		})
	if !errors.Is(err, errWarehouseTradeAccountStoreFailure) {
		t.Fatalf("error = %v, want %v", err, errWarehouseTradeAccountStoreFailure)
	}
	if !fault.failed || len(fault.saved) < 2 || fault.saved[0] != warehouseTradeAccountA || fault.saved[1] != warehouseTradeAccountB {
		t.Fatalf("fault order = %v (failed=%v), want the first account saved before the second failed", fault.saved, fault.failed)
	}
	before.requireEqual(t, warehouseTradeOwnerSnapshot(t, ctx, repos), "second account warehouse store failure")
}
