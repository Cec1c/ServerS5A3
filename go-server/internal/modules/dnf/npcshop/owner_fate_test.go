package npcshop

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func seedOwnerLedgerAccount(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:1",
		Metadata:  map[string]string{"keep": "account", "serial_counter": "0"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerAccountLedgerCommitsSameMutationShape(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	err = owner.Mutate(ctx, Command{
		AccountID:     "dnf:1",
		CharacterID:   "77",
		AccountLedger: true,
		Project: func(assets *Assets) (Changes, error) {
			assets.Account.Metadata["shop_test"] = "1"
			assets.Character.Stats["gold"] = 700
			assets.Inventory.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 600, Count: 3}
			assets.AccountInventory.Slots[dnfrepo.AccountSharedInventorySlotKey(3)] = dnfrepo.ItemStack{ItemID: 3033, Count: 2}
			return Changes{Account: true, AccountInventory: true, Character: true, Inventory: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	account, found, err := repositories.Account.Load(ctx, "dnf:1")
	if err != nil || !found || account.Metadata["keep"] != "account" || account.Metadata["shop_test"] != "1" {
		t.Fatalf("account missing mutation: found=%t account=%+v err=%v", found, account, err)
	}
	character, found, err := repositories.Character.Load(ctx, "77")
	if err != nil || !found || character.Stats["gold"] != 700 {
		t.Fatalf("character missing mutation: found=%t character=%+v err=%v", found, character, err)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "77")
	if err != nil || !found || inventory.Slots["0:10"].Count != 3 {
		t.Fatalf("inventory missing mutation: found=%t inventory=%+v err=%v", found, inventory, err)
	}
	accountInventory, found, err := repositories.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found || accountInventory.Slots[dnfrepo.AccountSharedInventorySlotKey(3)].Count != 2 {
		t.Fatalf("account inventory missing mutation: found=%t inventory=%+v err=%v", found, accountInventory, err)
	}
}

func TestOwnerAccountLedgerRejectsMissingAccount(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwnerWithoutAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = owner.Mutate(ctx, Command{
		AccountID:     "dnf:1",
		CharacterID:   "77",
		AccountLedger: true,
		Project: func(*Assets) (Changes, error) {
			called = true
			return Changes{Account: true}, nil
		},
	})
	if !errors.Is(err, dnfrepo.ErrAccountCharacterAssetTransactionUnavailable) || called {
		t.Fatalf("missing account err=%v called=%t", err, called)
	}
}

type ownerLedgerAccountInventoryFailure struct {
	inner dnfrepo.AccountCharacterLedgerAssetUnitOfWork
	err   error
}

type ownerBaseOnlyAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
}

func (u ownerLedgerAccountInventoryFailure) WithinAccountCharacterLedgerAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return u.inner.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(
		account dnfrepo.AccountRepository,
		accountInventory dnfrepo.AccountInventoryRepository,
		character dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		return apply(account, ownerFailAccountInventorySave{AccountInventoryRepository: accountInventory, err: u.err}, character, inventory, equipment)
	})
}

type ownerFailAccountInventorySave struct {
	dnfrepo.AccountInventoryRepository
	err error
}

func (r ownerFailAccountInventorySave) Save(ctx context.Context, record dnfrepo.AccountInventoryRecord) error {
	if err := r.AccountInventoryRepository.Save(ctx, record); err != nil {
		return err
	}
	return r.err
}

func TestOwnerAccountLedgerLastSaveFailureLeavesAllAssetsUnchanged(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, _ := repositories.Account.Load(ctx, "dnf:1")
	beforeAccountInventory, _, _ := repositories.AccountInventory.Load(ctx, "dnf:1")
	beforeCharacter, _, _ := repositories.Character.Load(ctx, "77")
	beforeInventory, _, _ := repositories.Inventory.Load(ctx, "77")
	wantErr := errors.New("fail last account-inventory save")
	owner.ledgerAssets = ownerLedgerAccountInventoryFailure{
		inner: repositories.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork),
		err:   wantErr,
	}
	err = owner.Mutate(ctx, Command{
		AccountID:     "dnf:1",
		CharacterID:   "77",
		AccountLedger: true,
		Project: func(assets *Assets) (Changes, error) {
			assets.Account.Metadata["last_save"] = "changed"
			assets.Character.Stats["gold"] = 1
			assets.Inventory.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 600, Count: 3}
			assets.AccountInventory.Slots[dnfrepo.AccountSharedInventorySlotKey(3)] = dnfrepo.ItemStack{ItemID: 3033, Count: 2}
			return Changes{Account: true, AccountInventory: true, Character: true, Inventory: true}, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("last save error=%v want=%v", err, wantErr)
	}
	afterAccount, _, _ := repositories.Account.Load(ctx, "dnf:1")
	afterAccountInventory, _, _ := repositories.AccountInventory.Load(ctx, "dnf:1")
	afterCharacter, _, _ := repositories.Character.Load(ctx, "77")
	afterInventory, _, _ := repositories.Inventory.Load(ctx, "77")
	if !reflect.DeepEqual(beforeAccount, afterAccount) ||
		!reflect.DeepEqual(beforeAccountInventory, afterAccountInventory) ||
		!reflect.DeepEqual(beforeCharacter, afterCharacter) ||
		!reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("last save failure leaked staged owner assets")
	}
}

func TestOwnerLedgerOnlyCapabilitySupportsFateMutation(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	repositories.CharacterSettlement = nil
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Mutate(ctx, Command{
		AccountID:     "dnf:1",
		CharacterID:   "77",
		AccountLedger: true,
		Project: func(assets *Assets) (Changes, error) {
			assets.Account.Metadata["ledger_only"] = "1"
			return Changes{Account: true}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	account, found, err := repositories.Account.Load(ctx, "dnf:1")
	if err != nil || !found || account.Metadata["ledger_only"] != "1" {
		t.Fatalf("ledger-only mutation found=%t account=%+v err=%v", found, account, err)
	}
}

func TestOwnerAccountLedgerRequiresLedgerCapabilityEvenWithSettlement(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	repositories.AccountAssets = ownerBaseOnlyAssets{AccountCharacterAssetUnitOfWork: repositories.AccountAssets}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = owner.Mutate(ctx, Command{
		AccountID:     "dnf:1",
		CharacterID:   "77",
		AccountLedger: true,
		Project: func(*Assets) (Changes, error) {
			called = true
			return Changes{Account: true}, nil
		},
	})
	if !errors.Is(err, ErrOwnerUnavailable) || called {
		t.Fatalf("ledger unavailable err=%v called=%t", err, called)
	}
}

func TestOwnerOrdinaryPurchasePrefersLedgerAndFaultHitsRealPath(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, _ := repositories.Account.Load(ctx, "dnf:1")
	wantErr := errors.New("ordinary ledger path fault")
	owner.ledgerAssets = ownerLedgerAccountInventoryFailure{
		inner: repositories.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork),
		err:   wantErr,
	}
	err = owner.Mutate(ctx, Command{
		AccountID:   "dnf:1",
		CharacterID: "77",
		Project: func(assets *Assets) (Changes, error) {
			assets.Account.Metadata["ordinary_ledger"] = "changed"
			assets.Character.Stats["gold"] = 1
			assets.Inventory.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 600, Count: 1}
			assets.AccountInventory.Slots[dnfrepo.AccountSharedInventorySlotKey(3)] = dnfrepo.ItemStack{ItemID: 3033, Count: 1}
			return Changes{Account: true, Character: true, Inventory: true, AccountInventory: true}, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ordinary purchase did not hit ledger fault: %v", err)
	}
	afterAccount, _, _ := repositories.Account.Load(ctx, "dnf:1")
	if !reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("ordinary preferred-ledger failure leaked account changes")
	}
}

func TestOwnerLedgerPreferredPurchaseLimitRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	repositories.CharacterSettlement = nil
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	policy := PurchaseLimit{ItemID: 600, Scope: "account", Maximum: 1, Count: 1}
	buy := func(fail bool) error {
		return owner.Mutate(ctx, Command{
			AccountID: "dnf:1", CharacterID: "77", PurchaseLimit: &policy,
			Project: func(assets *Assets) (Changes, error) {
				assets.Character.Stats["gold"] -= 100
				assets.Inventory.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 600, Count: 1}
				if fail {
					return Changes{}, errors.New("grant failed after ledger limit reservation")
				}
				return Changes{Character: true, Inventory: true}, nil
			},
		})
	}
	if err := buy(true); err == nil {
		t.Fatal("expected purchase rollback")
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	character, _, _ := repositories.Character.Load(ctx, "77")
	if PurchaseCount(account, character, policy, time.Now()) != 0 || character.Stats["gold"] != 1000 {
		t.Fatalf("failed ledger purchase leaked quota/assets account=%+v character=%+v", account, character)
	}
	if err := buy(false); err != nil {
		t.Fatal(err)
	}
	if err := buy(false); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("duplicate ledger purchase err=%v", err)
	}
	character, _, _ = repositories.Character.Load(ctx, "77")
	if character.Stats["gold"] != 900 {
		t.Fatalf("ledger purchase wallet=%d, want 900", character.Stats["gold"])
	}
}

func TestOwnerOrdinaryNoAccountCannotCreateOne(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwnerWithoutAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = owner.Mutate(ctx, Command{
		AccountID: "dnf:1", CharacterID: "77",
		Project: func(assets *Assets) (Changes, error) {
			called = true
			assets.Account.Metadata = map[string]string{"must_not_create": "1"}
			return Changes{Account: true}, nil
		},
	})
	if !errors.Is(err, dnfrepo.ErrAccountCharacterAssetTransactionUnavailable) || called {
		t.Fatalf("missing ledger account err=%v called=%t", err, called)
	}
	if _, found, loadErr := repositories.Account.Load(ctx, "dnf:1"); loadErr != nil || found {
		t.Fatalf("ordinary purchase created account: found=%t err=%v", found, loadErr)
	}
}

func TestOwnerLegacySettlementRejectsMissingAccountWithoutCreatingOne(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwnerWithoutAccount(t, ctx, repositories)
	repositories.AccountAssets = ownerBaseOnlyAssets{AccountCharacterAssetUnitOfWork: repositories.AccountAssets}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = owner.Mutate(ctx, Command{
		AccountID: "dnf:1", CharacterID: "77",
		Project: func(*Assets) (Changes, error) {
			called = true
			return Changes{Account: true}, nil
		},
	})
	if !errors.Is(err, ErrAccountNotFound) || called {
		t.Fatalf("missing settlement account err=%v called=%t", err, called)
	}
	if _, found, loadErr := repositories.Account.Load(ctx, "dnf:1"); loadErr != nil || found {
		t.Fatalf("legacy settlement created account: found=%t err=%v", found, loadErr)
	}
}

func TestNewOwnerRequiresAtLeastOneMutationCapability(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	repositories.AccountAssets = nil
	repositories.CharacterSettlement = nil
	if _, err := NewOwner(repositories); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("no-capability owner err=%v", err)
	}
}

func TestOwnerLegacySettlementStillCommitsWithoutLedgerCapability(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	repositories.AccountAssets = nil
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Mutate(ctx, Command{
		AccountID: "dnf:1", CharacterID: "77",
		Project: func(assets *Assets) (Changes, error) {
			assets.Character.Stats["gold"] -= 100
			assets.Inventory.Slots["0:10"] = dnfrepo.ItemStack{ItemID: 600, Count: 1}
			return Changes{Character: true, Inventory: true}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	character, _, _ := repositories.Character.Load(ctx, "77")
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	if character.Stats["gold"] != 900 || inventory.Slots["0:10"].Count != 1 {
		t.Fatalf("legacy mutation character=%+v inventory=%+v", character, inventory)
	}
}

func TestOwnerAccountLedgerSerializesSameAccountUpdates(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedNPCShopOwner(t, ctx, repositories)
	seedOwnerLedgerAccount(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 40
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- owner.Mutate(ctx, Command{
				AccountID:     "dnf:1",
				CharacterID:   "77",
				AccountLedger: true,
				Project: func(assets *Assets) (Changes, error) {
					n, err := strconv.Atoi(assets.Account.Metadata["serial_counter"])
					if err != nil {
						return Changes{}, err
					}
					assets.Account.Metadata["serial_counter"] = strconv.Itoa(n + 1)
					return Changes{Account: true}, nil
				},
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	if got := account.Metadata["serial_counter"]; got != strconv.Itoa(workers) {
		t.Fatalf("serial account counter=%q want=%d", got, workers)
	}
}
