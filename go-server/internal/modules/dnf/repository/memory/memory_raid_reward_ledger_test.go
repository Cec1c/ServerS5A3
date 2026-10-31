package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

const (
	ledgerMailboxMemoryAccountID    = "ledger-mailbox-account"
	ledgerMailboxMemoryOtherAccount = "ledger-mailbox-other-account"
	ledgerMailboxMemoryCharacterID  = "ledger-mailbox-character"
	ledgerMailboxMemoryOtherCharID  = "ledger-mailbox-other-character"
	ledgerMailboxMemoryCounterKey   = "test.ledger_mailbox.concurrent"
)

type ledgerMailboxMemorySnapshot struct {
	account          repo.AccountRecord
	accountInventory repo.AccountInventoryRecord
	character        repo.CharacterRecord
	inventory        repo.InventoryRecord
	equipment        repo.EquipmentRecord
	mailbox          repo.MailboxRecord
}

func newLedgerMailboxMemoryGroup(t *testing.T) repo.Group {
	t.Helper()
	ctx := context.Background()
	g := NewMemoryGroup()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: ledgerMailboxMemoryAccountID, Metadata: map[string]string{"keep": "account", ledgerMailboxMemoryCounterKey: "0"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: ledgerMailboxMemoryOtherAccount, Metadata: map[string]string{"keep": "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: ledgerMailboxMemoryAccountID, Slots: map[string]repo.ItemStack{"0:0": {ItemID: 1, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: ledgerMailboxMemoryCharacterID, AccountID: ledgerMailboxMemoryAccountID, Stats: map[string]int64{"keep": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: ledgerMailboxMemoryOtherCharID, AccountID: ledgerMailboxMemoryOtherAccount, Stats: map[string]int64{"keep": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ledgerMailboxMemoryCharacterID, Slots: map[string]repo.ItemStack{"0:0": {ItemID: 2, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: ledgerMailboxMemoryCharacterID, Entries: map[string]repo.EquipmentEntry{"0": {ItemID: 3, SlotIndex: 0}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: ledgerMailboxMemoryCharacterID, Mails: map[string]repo.MailRecord{"7": {MailID: "7", Title: "existing"}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: ledgerMailboxMemoryOtherCharID, Mails: map[string]repo.MailRecord{}}); err != nil {
		t.Fatal(err)
	}
	return g
}

func loadLedgerMailboxMemorySnapshot(t *testing.T, g repo.Group) ledgerMailboxMemorySnapshot {
	t.Helper()
	ctx := context.Background()
	account, found, err := g.Account.Load(ctx, ledgerMailboxMemoryAccountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	accountInventory, found, err := g.AccountInventory.Load(ctx, ledgerMailboxMemoryAccountID)
	if err != nil || !found {
		t.Fatalf("load account inventory: found=%t err=%v", found, err)
	}
	character, found, err := g.Character.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	inventory, found, err := g.Inventory.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, err)
	}
	equipment, found, err := g.Equipment.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil || !found {
		t.Fatalf("load equipment: found=%t err=%v", found, err)
	}
	mailbox, found, err := g.Mailbox.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil || !found {
		t.Fatalf("load mailbox: found=%t err=%v", found, err)
	}
	return ledgerMailboxMemorySnapshot{
		account:          account,
		accountInventory: accountInventory,
		character:        character,
		inventory:        inventory,
		equipment:        equipment,
		mailbox:          mailbox,
	}
}

func applyLedgerMailboxMemoryMutation(
	ctx context.Context,
	accounts repo.AccountRepository,
	accountInventory repo.AccountInventoryRepository,
	characters repo.CharacterRepository,
	inventories repo.InventoryRepository,
	equipment repo.EquipmentRepository,
	mailboxes repo.MailboxRepository,
) error {
	account, _, err := accounts.Load(ctx, ledgerMailboxMemoryAccountID)
	if err != nil {
		return err
	}
	account.Metadata["fate.weekly"] = "2500"
	if err = accounts.Save(ctx, account); err != nil {
		return err
	}
	accountInv, _, err := accountInventory.Load(ctx, ledgerMailboxMemoryAccountID)
	if err != nil {
		return err
	}
	accountInv.Slots["0:1"] = repo.ItemStack{ItemID: 4, Count: 2}
	if err = accountInventory.Save(ctx, accountInv); err != nil {
		return err
	}
	character, _, err := characters.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil {
		return err
	}
	character.Stats["raid_receipt"] = 1
	if err = characters.Save(ctx, character); err != nil {
		return err
	}
	inventory, _, err := inventories.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil {
		return err
	}
	inventory.Slots["0:1"] = repo.ItemStack{ItemID: 10304529, Count: 1}
	if err = inventories.Save(ctx, inventory); err != nil {
		return err
	}
	gear, _, err := equipment.Load(ctx, ledgerMailboxMemoryCharacterID)
	if err != nil {
		return err
	}
	gear.Entries["1"] = repo.EquipmentEntry{ItemID: 5, SlotIndex: 1}
	if err = equipment.Save(ctx, gear); err != nil {
		return err
	}
	_, err = repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{
		RecipientCharacterID: ledgerMailboxMemoryCharacterID,
		Source:               "ledger_mailbox_memory_test",
		Title:                "raid overflow",
		Attachments:          []repo.MailAttachment{{ItemID: 10304529, Count: 1}},
	})
	return err
}

func TestLedgerMailboxMemoryCommitsMetadataCharacterAndMail(t *testing.T) {
	ctx := context.Background()
	g := newLedgerMailboxMemoryGroup(t)
	u := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	if err := u.WithinAccountCharacterLedgerMailboxAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
		a repo.AccountRepository,
		ai repo.AccountInventoryRepository,
		c repo.CharacterRepository,
		i repo.InventoryRepository,
		e repo.EquipmentRepository,
		m repo.MailboxRepository,
	) error {
		return applyLedgerMailboxMemoryMutation(ctx, a, ai, c, i, e, m)
	}); err != nil {
		t.Fatal(err)
	}

	account, _, _ := g.Account.Load(ctx, ledgerMailboxMemoryAccountID)
	if account.Metadata["keep"] != "account" || account.Metadata["fate.weekly"] != "2500" {
		t.Fatalf("account metadata not committed atomically: %#v", account.Metadata)
	}
	accountInventory, _, _ := g.AccountInventory.Load(ctx, ledgerMailboxMemoryAccountID)
	if accountInventory.Slots["0:1"].ItemID != 4 || accountInventory.Slots["0:1"].Count != 2 {
		t.Fatalf("account reward not committed: %#v", accountInventory.Slots)
	}
	character, _, _ := g.Character.Load(ctx, ledgerMailboxMemoryCharacterID)
	if character.Stats["keep"] != 1 || character.Stats["raid_receipt"] != 1 {
		t.Fatalf("character receipt not committed: %#v", character.Stats)
	}
	inventory, _, _ := g.Inventory.Load(ctx, ledgerMailboxMemoryCharacterID)
	if inventory.Slots["0:1"].ItemID != 10304529 || inventory.Slots["0:1"].Count != 1 {
		t.Fatalf("character reward not committed: %#v", inventory.Slots)
	}
	equipment, _, _ := g.Equipment.Load(ctx, ledgerMailboxMemoryCharacterID)
	if entry, ok := equipment.Entries["1"]; !ok || entry.ItemID != 5 || entry.SlotIndex != 1 {
		t.Fatalf("equipment reward not committed: %#v", equipment.Entries)
	}
	mailbox, _, _ := g.Mailbox.Load(ctx, ledgerMailboxMemoryCharacterID)
	if len(mailbox.Mails) != 2 {
		t.Fatalf("overflow mail not committed: %#v", mailbox.Mails)
	}
	foundOverflow := false
	for _, mail := range mailbox.Mails {
		if mail.Metadata["source"] == "ledger_mailbox_memory_test" {
			foundOverflow = len(mail.Attachments) == 1 && mail.Attachments[0].ItemID == 10304529
		}
	}
	if !foundOverflow {
		t.Fatalf("overflow attachment missing: %#v", mailbox.Mails)
	}
}

func TestLedgerMailboxMemoryCallbackFailuresLeaveEveryAggregateUnchanged(t *testing.T) {
	callbackErr := errors.New("ledger mailbox callback failure")
	panicErr := errors.New("ledger mailbox callback panic")
	for _, scenario := range []string{"callback_error", "panic", "context_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			g := newLedgerMailboxMemoryGroup(t)
			before := loadLedgerMailboxMemorySnapshot(t, g)
			u := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = u.WithinAccountCharacterLedgerMailboxAssets(runCtx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
					a repo.AccountRepository,
					ai repo.AccountInventoryRepository,
					c repo.CharacterRepository,
					i repo.InventoryRepository,
					e repo.EquipmentRepository,
					m repo.MailboxRepository,
				) error {
					if applyErr := applyLedgerMailboxMemoryMutation(runCtx, a, ai, c, i, e, m); applyErr != nil {
						return applyErr
					}
					switch scenario {
					case "callback_error":
						return callbackErr
					case "panic":
						panic(panicErr)
					case "context_cancel":
						cancel()
					}
					return nil
				})
			}()
			switch scenario {
			case "callback_error":
				if recovered != nil || !errors.Is(err, callbackErr) {
					t.Fatalf("callback error not preserved: panic=%v err=%v", recovered, err)
				}
			case "panic":
				if recovered != panicErr {
					t.Fatalf("panic not preserved: panic=%v err=%v", recovered, err)
				}
			case "context_cancel":
				if recovered != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation not preserved: panic=%v err=%v", recovered, err)
				}
			}
			after := loadLedgerMailboxMemorySnapshot(t, g)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("failure leaked aggregate state\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestLedgerMailboxMemoryRejectsOwnershipAndExistenceViolations(t *testing.T) {
	ctx := context.Background()
	g := newLedgerMailboxMemoryGroup(t)
	u := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)

	called := false
	err := u.WithinAccountCharacterLedgerMailboxAssets(ctx, "missing-ledger-mailbox-account", ledgerMailboxMemoryCharacterID, func(
		repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository,
	) error {
		called = true
		return nil
	})
	if !errors.Is(err, repo.ErrAccountCharacterAssetTransactionUnavailable) || called {
		t.Fatalf("missing account accepted: called=%t err=%v", called, err)
	}

	called = false
	err = u.WithinAccountCharacterLedgerMailboxAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryOtherCharID, func(
		repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository,
	) error {
		called = true
		return nil
	})
	if !errors.Is(err, repo.ErrAccountCharacterMailboxOwnership) || called {
		t.Fatalf("foreign character accepted: called=%t err=%v", called, err)
	}
}

func TestLedgerMailboxMemoryScopesEveryRepositoryKey(t *testing.T) {
	ctx := context.Background()
	g := newLedgerMailboxMemoryGroup(t)
	u := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	var escapes []string
	err := u.WithinAccountCharacterLedgerMailboxAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
		accounts repo.AccountRepository,
		accountInventory repo.AccountInventoryRepository,
		characters repo.CharacterRepository,
		inventories repo.InventoryRepository,
		equipment repo.EquipmentRepository,
		mailboxes repo.MailboxRepository,
	) error {
		if _, _, loadErr := accounts.Load(ctx, ledgerMailboxMemoryOtherAccount); loadErr == nil {
			escapes = append(escapes, "account")
		}
		if _, _, loadErr := accountInventory.Load(ctx, ledgerMailboxMemoryOtherAccount); loadErr == nil {
			escapes = append(escapes, "account_inventory")
		}
		if _, _, loadErr := characters.Load(ctx, ledgerMailboxMemoryOtherCharID); loadErr == nil {
			escapes = append(escapes, "character")
		}
		if _, _, loadErr := inventories.Load(ctx, ledgerMailboxMemoryOtherCharID); loadErr == nil {
			escapes = append(escapes, "inventory")
		}
		if _, _, loadErr := equipment.Load(ctx, ledgerMailboxMemoryOtherCharID); loadErr == nil {
			escapes = append(escapes, "equipment")
		}
		if _, _, loadErr := mailboxes.Load(ctx, ledgerMailboxMemoryOtherCharID); loadErr == nil {
			escapes = append(escapes, "mailbox")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(escapes) != 0 {
		t.Fatalf("cross-owner repository access escaped: %v", escapes)
	}
}

var errLedgerMailboxMemoryInjectedSave = errors.New("ledger mailbox injected post-save failure")

type ledgerMailboxMemoryFailAfterSave struct {
	repo.MailboxRepository
	fail bool
	hit  bool
}

func (m *ledgerMailboxMemoryFailAfterSave) Save(ctx context.Context, record repo.MailboxRecord) error {
	if err := m.MailboxRepository.Save(ctx, record); err != nil {
		return err
	}
	if m.fail {
		m.fail = false
		m.hit = true
		return errLedgerMailboxMemoryInjectedSave
	}
	return nil
}

func TestLedgerMailboxMemoryLateMailboxSaveFailureRestoresAttemptedAggregates(t *testing.T) {
	ctx := context.Background()
	g := newLedgerMailboxMemoryGroup(t)
	before := loadLedgerMailboxMemorySnapshot(t, g)
	owner := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
	// The final account-ledger field is a concrete *memoryAccountStore, so a
	// test cannot replace only its MemoryStore.Save without production changes.
	// Mailbox is the last interface-backed write and still exercises the real
	// transaction's post-save failure and reverse-restore path.
	fault := &ledgerMailboxMemoryFailAfterSave{MailboxRepository: g.Mailbox, fail: true}
	owner.mailbox = fault
	u := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	err := u.WithinAccountCharacterLedgerMailboxAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
		accounts repo.AccountRepository,
		accountInventory repo.AccountInventoryRepository,
		characters repo.CharacterRepository,
		inventories repo.InventoryRepository,
		equipment repo.EquipmentRepository,
		mailboxes repo.MailboxRepository,
	) error {
		return applyLedgerMailboxMemoryMutation(ctx, accounts, accountInventory, characters, inventories, equipment, mailboxes)
	})
	if !fault.hit || !errors.Is(err, errLedgerMailboxMemoryInjectedSave) {
		t.Fatalf("late mailbox failure not exercised: hit=%t err=%v", fault.hit, err)
	}
	after := loadLedgerMailboxMemorySnapshot(t, g)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("late mailbox failure leaked aggregate state\nbefore=%#v\nafter=%#v", before, after)
	}
}

func incrementLedgerMailboxMemoryCounter(ctx context.Context, accounts repo.AccountRepository) error {
	account, found, err := accounts.Load(ctx, ledgerMailboxMemoryAccountID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("ledger mailbox account disappeared")
	}
	current, err := strconv.Atoi(account.Metadata[ledgerMailboxMemoryCounterKey])
	if err != nil {
		return err
	}
	account.Metadata[ledgerMailboxMemoryCounterKey] = strconv.Itoa(current + 1)
	return accounts.Save(ctx, account)
}

func TestLedgerMailboxMemoryConcurrentLedgerDoesNotLoseUpdates(t *testing.T) {
	ctx := context.Background()
	g := newLedgerMailboxMemoryGroup(t)
	ledger := g.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)
	ledgerMailbox := g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork)
	const rounds = 20
	errCh := make(chan error, rounds*2)
	var wg sync.WaitGroup
	for n := 0; n < rounds; n++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errCh <- ledger.WithinAccountCharacterLedgerAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
				accounts repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository,
			) error {
				return incrementLedgerMailboxMemoryCounter(ctx, accounts)
			})
		}()
		go func() {
			defer wg.Done()
			errCh <- ledgerMailbox.WithinAccountCharacterLedgerMailboxAssets(ctx, ledgerMailboxMemoryAccountID, ledgerMailboxMemoryCharacterID, func(
				accounts repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.MailboxRepository,
			) error {
				return incrementLedgerMailboxMemoryCounter(ctx, accounts)
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
	account, _, _ := g.Account.Load(ctx, ledgerMailboxMemoryAccountID)
	want := strconv.Itoa(rounds * 2)
	if account.Metadata[ledgerMailboxMemoryCounterKey] != want {
		t.Fatalf("lost account metadata update: got=%q want=%q metadata=%#v", account.Metadata[ledgerMailboxMemoryCounterKey], want, account.Metadata)
	}
}
