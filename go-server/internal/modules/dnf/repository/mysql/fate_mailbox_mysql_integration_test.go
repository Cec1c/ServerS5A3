//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	fateMailboxAccountA   = "fate-mailbox-account-a"
	fateMailboxAccountB   = "fate-mailbox-account-b"
	fateMailboxCharacterA = "fate-mailbox-character-a"
	fateMailboxCharacterB = "fate-mailbox-character-b"
	fateMailboxCharacterC = "fate-mailbox-character-c"
	fateMailboxItemSlot   = "0:1"
)

func TestMySQLFateLedgerMailboxAndTradeMailboxInterleavings(t *testing.T) {
	if os.Getenv("DNF_FATE_DISPOSABLE_DB") != "1" {
		t.Fatal("requires an explicitly isolated Fate test database instance")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" {
		t.Fatal("invalid isolated loopback DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("test requires loopback and no existing database name")
	}
	cfg.ParseTime = true
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	open := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open isolated database")
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	observer, left, right := open(), open(), open()
	databaseName := fmt.Sprintf("fate_mailbox_%d", time.Now().UnixNano())
	if !regexp.MustCompile(`^fate_mailbox_[0-9]+$`).MatchString(databaseName) {
		t.Fatal("unsafe fixture name")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := observer.ExecContext(cleanup, "DROP DATABASE IF EXISTS `"+databaseName+"`"); err != nil {
			t.Error(err)
		}
	})
	plan := repo.DatabasePlan{ReadDatabases: []string{databaseName}, WriteDatabases: []string{databaseName}}
	if err := EnsureMySQLSchema(ctx, observer, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	group := func(db *sql.DB) repo.Group {
		t.Helper()
		g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	leftGroup, rightGroup := group(left), group(right)
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	seed := func() {
		t.Helper()
		accountA := repo.AccountRecord{AccountID: fateMailboxAccountA, State: "active", Metadata: map[string]string{"keep": "original"}}
		for _, account := range []repo.AccountRecord{
			accountA,
			{AccountID: fateMailboxAccountB, State: "active", Metadata: map[string]string{"keep": "other"}},
		} {
			if err := leftGroup.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
		}
		for _, character := range []repo.CharacterRecord{
			{CharacterID: fateMailboxCharacterA, AccountID: fateMailboxAccountA, Slot: 0, Name: "FateMailA", Job: "0", Level: 90, Stats: map[string]int64{"gold": 1}},
			{CharacterID: fateMailboxCharacterB, AccountID: fateMailboxAccountB, Slot: 0, Name: "FateMailB", Job: "0", Level: 90, Stats: map[string]int64{"gold": 2}},
			{CharacterID: fateMailboxCharacterC, AccountID: fateMailboxAccountA, Slot: 1, Name: "FateMailC", Job: "0", Level: 90, Stats: map[string]int64{"gold": 3}},
		} {
			if err := leftGroup.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
		}
		for _, characterID := range []string{fateMailboxCharacterA, fateMailboxCharacterB, fateMailboxCharacterC} {
			if err := leftGroup.Inventory.Save(ctx, repo.InventoryRecord{
				CharacterID: characterID,
				Slots:       map[string]repo.ItemStack{fateMailboxItemSlot: {ItemID: 1001, Count: 3}},
			}); err != nil {
				t.Fatal(err)
			}
			if err := leftGroup.Mailbox.Save(ctx, repo.MailboxRecord{
				CharacterID: characterID,
				Mails:       map[string]repo.MailRecord{},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	ledgerMailbox := func(g repo.Group, accountID, characterID string, barrier func() error, tag string, injected error) error {
		return g.AccountAssets.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork).WithinAccountCharacterLedgerMailboxAssets(ctx, accountID, characterID, func(
			accounts repo.AccountRepository,
			_ repo.AccountInventoryRepository,
			characters repo.CharacterRepository,
			inventories repo.InventoryRepository,
			_ repo.EquipmentRepository,
			mailboxes repo.MailboxRepository,
		) error {
			if err := barrier(); err != nil {
				return err
			}
			if err := fateMailboxMutate(ctx, accounts, characters, inventories, mailboxes, accountID, characterID, tag, now); err != nil {
				return err
			}
			return injected
		})
	}
	tradeMailbox := func(g repo.Group, accountID string, characterIDs []string, mailID string, barrier func() error, tag string, injected error) error {
		return g.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork).WithinAccountTradeMailbox(ctx, accountID, characterIDs, mailID, func(
			accounts repo.AccountRepository,
			characters repo.CharacterRepository,
			inventories repo.InventoryRepository,
			mailboxes repo.MailboxRepository,
		) error {
			if err := barrier(); err != nil {
				return err
			}
			if err := fateMailboxMutate(ctx, accounts, characters, inventories, mailboxes, accountID, mailID, tag, now); err != nil {
				return err
			}
			return injected
		})
	}

	for _, ledgerFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("ledger-first-%t", ledgerFirst), func(t *testing.T) {
			seed()
			leftID := fateMailboxConnectionID(t, ctx, left)
			rightID := fateMailboxConnectionID(t, ctx, right)
			if leftID == rightID {
				t.Fatal("not independent database connections")
			}
			first := func(barrier func() error) error {
				return ledgerMailbox(leftGroup, fateMailboxAccountA, fateMailboxCharacterA, barrier, "ledger", nil)
			}
			second := func(barrier func() error) error {
				return tradeMailbox(rightGroup, fateMailboxAccountA, []string{fateMailboxCharacterC, fateMailboxCharacterA}, fateMailboxCharacterA, barrier, "trade", nil)
			}
			if !ledgerFirst {
				first = func(barrier func() error) error {
					return tradeMailbox(leftGroup, fateMailboxAccountA, []string{fateMailboxCharacterC, fateMailboxCharacterA}, fateMailboxCharacterA, barrier, "trade", nil)
				}
				second = func(barrier func() error) error {
					return ledgerMailbox(rightGroup, fateMailboxAccountA, fateMailboxCharacterA, barrier, "ledger", nil)
				}
			}
			locked, release := make(chan struct{}), make(chan struct{})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- first(func() error {
					close(locked)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-locked:
			case err := <-firstDone:
				t.Fatalf("first transaction before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() { secondDone <- second(func() error { return nil }) }()
			waitErr := fateMailboxAwaitLock(ctx, observer, rightID, leftID)
			close(release)
			e1, e2 := <-firstDone, <-secondDone
			if waitErr != nil || e1 != nil || e2 != nil {
				t.Fatalf("wait=%v first=%v second=%v", waitErr, e1, e2)
			}
			fateMailboxAssertCommitted(t, ctx, leftGroup, now, "ledger", "trade")
		})
	}

	wrongOwnership := errors.New("wrong ownership callback unexpectedly ran")
	for _, name := range []string{"ledger-mailbox", "trade-mailbox"} {
		t.Run(name+"/wrong-ownership", func(t *testing.T) {
			seed()
			callbackRan := false
			barrier := func() error { callbackRan = true; return wrongOwnership }
			var err error
			if name == "ledger-mailbox" {
				err = ledgerMailbox(leftGroup, fateMailboxAccountA, fateMailboxCharacterB, barrier, "wrong", nil)
			} else {
				err = tradeMailbox(leftGroup, fateMailboxAccountA, []string{fateMailboxCharacterA, fateMailboxCharacterB}, fateMailboxCharacterB, barrier, "wrong", nil)
			}
			if !errors.Is(err, repo.ErrAccountCharacterMailboxOwnership) {
				t.Fatalf("error=%v want ownership rejection", err)
			}
			if callbackRan {
				t.Fatal("ownership-mismatched callback ran")
			}
			fateMailboxAssertUnchanged(t, ctx, leftGroup, now)
		})
	}

	for _, name := range []string{"ledger-mailbox", "trade-mailbox"} {
		t.Run(name+"/rollback", func(t *testing.T) {
			seed()
			injected := errors.New("injected " + name + " callback failure")
			var err error
			if name == "ledger-mailbox" {
				err = ledgerMailbox(leftGroup, fateMailboxAccountA, fateMailboxCharacterA, func() error { return nil }, "rollback", injected)
			} else {
				err = tradeMailbox(leftGroup, fateMailboxAccountA, []string{fateMailboxCharacterA}, fateMailboxCharacterA, func() error { return nil }, "rollback", injected)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("error=%v want injected rollback", err)
			}
			fateMailboxAssertUnchanged(t, ctx, leftGroup, now)
		})
	}
}

func fateMailboxMutate(
	ctx context.Context,
	accounts repo.AccountRepository,
	characters repo.CharacterRepository,
	inventories repo.InventoryRepository,
	mailboxes repo.MailboxRepository,
	accountID, characterID, tag string,
	now time.Time,
) error {
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.AccountID != accountID {
		return fmt.Errorf("character ownership changed")
	}
	account, found, err := accounts.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("account missing")
	}
	account = repo.CloneAccount(account)
	granted, _, err := raidcurrency.Credit(&account, 37, now)
	if err != nil {
		return err
	}
	if granted != 37 {
		return fmt.Errorf("Fate grant=%d want=37", granted)
	}
	account.UpdatedAt = now
	if err := accounts.Save(ctx, account); err != nil {
		return err
	}
	inventory, found, err := inventories.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("inventory missing")
	}
	inventory = repo.CloneInventory(inventory)
	if inventory.Slots == nil {
		inventory.Slots = map[string]repo.ItemStack{}
	}
	stack := inventory.Slots[fateMailboxItemSlot]
	if stack.ItemID != 1001 || stack.Count < 1 {
		return fmt.Errorf("source stack=%+v", stack)
	}
	stack.Count--
	if stack.Count == 0 {
		delete(inventory.Slots, fateMailboxItemSlot)
	} else {
		inventory.Slots[fateMailboxItemSlot] = stack
	}
	inventory.UpdatedAt = now
	if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
		return err
	}
	_, err = repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{
		RecipientCharacterID: characterID,
		Title:                "fate mailbox integration",
		Body:                 tag,
		Source:               "fate-mailbox-integration:" + tag,
		Attachments:          []repo.MailAttachment{{ItemID: 1001, Count: 1}},
		CreatedAt:            now,
	})
	return err
}

func fateMailboxAssertCommitted(t *testing.T, ctx context.Context, g repo.Group, now time.Time, tags ...string) {
	t.Helper()
	account, found, err := g.Account.Load(ctx, fateMailboxAccountA)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 74 || state.Earned != 74 || account.Metadata["keep"] != "original" {
		t.Fatalf("Fate state=%+v metadata=%v err=%v", state, account.Metadata, err)
	}
	inventory, found, err := g.Inventory.Load(ctx, fateMailboxCharacterA)
	if err != nil || !found || inventory.Slots[fateMailboxItemSlot].Count != 1 {
		t.Fatalf("inventory found=%t record=%+v err=%v", found, inventory, err)
	}
	mailbox, found, err := g.Mailbox.Load(ctx, fateMailboxCharacterA)
	if err != nil || !found || len(mailbox.Mails) != len(tags) {
		t.Fatalf("mailbox found=%t mails=%d want=%d err=%v", found, len(mailbox.Mails), len(tags), err)
	}
	sources := map[string]bool{}
	for _, mail := range mailbox.Mails {
		if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 1001 || mail.Attachments[0].Count != 1 {
			t.Fatalf("mail attachment=%+v", mail.Attachments)
		}
		sources[mail.Metadata["source"]] = true
	}
	for _, tag := range tags {
		if !sources["fate-mailbox-integration:"+tag] {
			t.Fatalf("missing committed mail source %q: %v", tag, sources)
		}
	}
	if other, found, err := g.Inventory.Load(ctx, fateMailboxCharacterC); err != nil || !found || other.Slots[fateMailboxItemSlot].Count != 3 {
		t.Fatalf("same-account non-mail character changed found=%t record=%+v err=%v", found, other, err)
	}
}

func fateMailboxAssertUnchanged(t *testing.T, ctx context.Context, g repo.Group, now time.Time) {
	t.Helper()
	account, found, err := g.Account.Load(ctx, fateMailboxAccountA)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	state, err := raidcurrency.Read(account, now)
	if err != nil || state.Balance != 0 || state.Earned != 0 || account.Metadata["keep"] != "original" {
		t.Fatalf("account changed state=%+v metadata=%v err=%v", state, account.Metadata, err)
	}
	inventory, found, err := g.Inventory.Load(ctx, fateMailboxCharacterA)
	if err != nil || !found || inventory.Slots[fateMailboxItemSlot].ItemID != 1001 || inventory.Slots[fateMailboxItemSlot].Count != 3 {
		t.Fatalf("inventory changed found=%t record=%+v err=%v", found, inventory, err)
	}
	mailbox, found, err := g.Mailbox.Load(ctx, fateMailboxCharacterA)
	if err != nil || !found || len(mailbox.Mails) != 0 {
		t.Fatalf("mailbox changed found=%t mails=%d err=%v", found, len(mailbox.Mails), err)
	}
	otherAccount, found, err := g.Account.Load(ctx, fateMailboxAccountB)
	if err != nil || !found || otherAccount.Metadata["keep"] != "other" {
		t.Fatalf("foreign account changed found=%t account=%+v err=%v", found, otherAccount, err)
	}
	otherInventory, found, err := g.Inventory.Load(ctx, fateMailboxCharacterB)
	if err != nil || !found || otherInventory.Slots[fateMailboxItemSlot].ItemID != 1001 || otherInventory.Slots[fateMailboxItemSlot].Count != 3 {
		t.Fatalf("foreign inventory changed found=%t record=%+v err=%v", found, otherInventory, err)
	}
	otherMailbox, found, err := g.Mailbox.Load(ctx, fateMailboxCharacterB)
	if err != nil || !found || len(otherMailbox.Mails) != 0 {
		t.Fatalf("foreign mailbox changed found=%t mails=%d err=%v", found, len(otherMailbox.Mails), err)
	}
}

func fateMailboxConnectionID(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func fateMailboxAwaitLock(ctx context.Context, observer *sql.DB, requester, blocker int64) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		var count int
		err := observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.INNODB_LOCK_WAITS w JOIN information_schema.INNODB_TRX r ON r.trx_id=w.requesting_trx_id JOIN information_schema.INNODB_TRX b ON b.trx_id=w.blocking_trx_id WHERE r.trx_mysql_thread_id=? AND b.trx_mysql_thread_id=?`, requester, blocker).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	return fmt.Errorf("no actual InnoDB lock wait from %d to %d", requester, blocker)
}
