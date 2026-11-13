package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// These tests drive the production WithinMailboxAssets transaction through the
// row model connector already used by the account item delta suite. They only
// touch the single assetCharacterID inventory that the unit of work exposes;
// the mailbox character's inventory rows are seeded to prove they stay
// untouched.
const (
	mailboxDeltaAssetOwner   = "77"
	mailboxDeltaMailboxOwner = "88"
)

func mailboxDeltaUnitOfWork(state *cargoDeltaSQLState) (*mysqlMailboxAssetUnitOfWork, *sql.DB) {
	database := sql.OpenDB(cargoDeltaConnector{state})
	u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"replica"},
		writeDBs:    []string{"dnf_s1_w1"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
	return u, database
}

func seedMailboxDeltaInventory(t *testing.T, db *rowModelDB, characterID string, rows int) repository.InventoryRecord {
	t.Helper()
	record := repository.InventoryRecord{
		CharacterID: characterID,
		Slots:       map[string]repository.ItemStack{},
		Warehouse:   map[string]repository.ItemStack{},
	}
	for n := 0; n < rows; n++ {
		record.Slots[bagMoveSlotKey(0, int16(n))] = bagMoveStackable(int64(700000+n), 5)
	}
	record.Warehouse[bagMoveSlotKey(2, 0)] = bagMoveEquipStack(900000)
	record.Warehouse[bagMoveSlotKey(2, 1)] = bagMoveStackable(900001, 4)
	if err := newRowModelGroup(t, db, false).Inventory.Save(context.Background(), record); err != nil {
		t.Fatalf("seed %s inventory: %v", characterID, err)
	}
	db.calls = nil // Measure the transaction under test, not fixture creation.
	return record
}

func mailboxDeltaWrites(calls []fakeSQLCall) []fakeSQLCall {
	out := make([]fakeSQLCall, 0, len(calls))
	for _, call := range calls {
		if strings.HasPrefix(call.Query, "SELECT ") {
			continue
		}
		out = append(out, call)
	}
	return out
}

func mailboxDeltaWriteStats(calls []fakeSQLCall) (statements, parameters int) {
	for _, call := range mailboxDeltaWrites(calls) {
		statements++
		parameters += len(call.Args)
	}
	return statements, parameters
}

// mailboxDeltaOwnerLines keeps every snapshot line belonging to one character
// row (inventories, inventory_items and inventory_item_extra). Parent rows have
// no trailing columns; item rows start with the same "character_id=" prefix.
func mailboxDeltaOwnerLines(snapshot map[string][]string, owner string) map[string][]string {
	out := map[string][]string{}
	exact := "character_id=" + owner
	withNext := exact + ","
	for table, lines := range snapshot {
		for _, line := range lines {
			// Parent rows hold only the owner id; item rows sort bind_flag
			// first, so the owner field can appear anywhere in the line.
			if line == exact || strings.Contains(line, withNext) {
				out[table] = append(out[table], line)
			}
		}
	}
	return out
}

func mailboxDeltaWithoutEntry(lines map[string][]string, entryKey string) map[string][]string {
	out := map[string][]string{}
	needle := "entry_key=" + entryKey + ","
	for table, rows := range lines {
		kept := make([]string, 0, len(rows))
		for _, row := range rows {
			if strings.Contains(row, needle) {
				continue
			}
			kept = append(kept, row)
		}
		if len(kept) > 0 {
			out[table] = kept
		}
	}
	return out
}

func TestMySQLMailboxAssetInventoryDeltaWritesOnlyChangedSlot(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	const rows = 40
	const changedKey = "0:1"

	var snapshots []map[string][]string
	var statements, parameters []int
	for _, optimized := range []bool{false, true} {
		db := newRowModelDB()
		seedMailboxDeltaInventory(t, db, mailboxDeltaAssetOwner, rows)
		seedMailboxDeltaInventory(t, db, mailboxDeltaMailboxOwner, 3)
		before := db.model.snapshot()
		state := &cargoDeltaSQLState{db: db}
		u, sqlDB := mailboxDeltaUnitOfWork(state)
		err := u.WithinMailboxAssets(ctx, mailboxDeltaAssetOwner, mailboxDeltaMailboxOwner, func(_ repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.MailboxRepository) error {
			bag := inventories.(*characterSettlementScopedInventoryRepository).base.(*mysqlBagStore)
			if !optimized {
				bag.trackSlotChanges = false
			} else if !bag.trackSlotChanges {
				return fmt.Errorf("mailbox transaction did not enable slot delta tracking")
			}
			record, found, loadErr := inventories.Load(ctx, mailboxDeltaAssetOwner)
			if loadErr != nil || !found {
				return fmt.Errorf("load asset inventory found=%v err=%w", found, loadErr)
			}
			item := record.Slots[changedKey]
			item.Count++
			record.Slots[changedKey] = item
			return repository.SaveInventoryFields(ctx, inventories, record, repository.InventoryFieldSlots)
		})
		sqlDB.Close()
		if err != nil {
			t.Fatalf("optimized=%t mailbox transaction: %v", optimized, err)
		}
		if state.commits != 1 || state.rollbacks != 0 {
			t.Fatalf("optimized=%t commits=%d rollbacks=%d", optimized, state.commits, state.rollbacks)
		}
		after := db.model.snapshot()
		if want, got := mailboxDeltaOwnerLines(before, mailboxDeltaMailboxOwner), mailboxDeltaOwnerLines(after, mailboxDeltaMailboxOwner); !reflect.DeepEqual(want, got) {
			t.Fatalf("optimized=%t rewrote the mailbox owner's inventory", optimized)
		}
		if want, got := mailboxDeltaWithoutEntry(mailboxDeltaOwnerLines(before, mailboxDeltaAssetOwner), changedKey), mailboxDeltaWithoutEntry(mailboxDeltaOwnerLines(after, mailboxDeltaAssetOwner), changedKey); !reflect.DeepEqual(want, got) {
			t.Fatalf("optimized=%t rewrote untouched asset slots", optimized)
		}
		unchanged := []string{"0:0", "0:2", fmt.Sprintf("0:%d", rows-1), "2:0", "2:1"}
		inDeletes := 0
		for _, call := range mailboxDeltaWrites(db.calls) {
			characterInventory := strings.Contains(call.Query, "dnf_inventory_items") || strings.Contains(call.Query, "dnf_inventory_item_extra")
			if characterInventory && strings.HasPrefix(call.Query, "DELETE FROM ") {
				if strings.Contains(call.Query, "entry_key IN (") {
					inDeletes++
					if !optimized {
						t.Fatalf("full rewrite used a slot delta delete: %s", call.Query)
					}
					if !reflect.DeepEqual(call.Args, []any{mailboxDeltaAssetOwner, "slots", changedKey}) {
						t.Fatalf("slot delta delete touched more than %q: %v", changedKey, call.Args)
					}
				} else if optimized {
					t.Fatalf("delta save rewrote the whole container: %s", call.Query)
				}
			}
			for _, arg := range call.Args {
				value, ok := arg.(string)
				if !ok {
					continue
				}
				if value == mailboxDeltaMailboxOwner {
					t.Fatalf("optimized=%t wrote the other owner: %s", optimized, call.Query)
				}
				if optimized {
					for _, key := range unchanged {
						if value == key {
							t.Fatalf("delta wrote untouched key %q: %s", key, call.Query)
						}
					}
				}
			}
		}
		if optimized && inDeletes != 2 {
			t.Fatalf("delta deletes=%d, want one per character inventory table", inDeletes)
		}
		snapshots = append(snapshots, after)
		written, args := mailboxDeltaWriteStats(db.calls)
		statements = append(statements, written)
		parameters = append(parameters, args)
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatal("slot delta rows differ from the full rewrite")
	}
	if statements[1] >= statements[0] || parameters[1]*2 >= parameters[0] {
		t.Fatalf("mailbox delta did not reduce write amplification: statements %d->%d parameters %d->%d", statements[0], statements[1], parameters[0], parameters[1])
	}
	t.Logf("mailbox escrow save: statements %d->%d, parameters %d->%d; row state equal", statements[0], statements[1], parameters[0], parameters[1])
}

func TestMySQLMailboxAssetInventoryDeltaFallsBackWithoutBeforeImage(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	const rows = 12
	const changedKey = "0:0"
	for _, mode := range []string{"unloaded", "snapshot_cleared"} {
		t.Run(mode, func(t *testing.T) {
			db := newRowModelDB()
			seeded := seedMailboxDeltaInventory(t, db, mailboxDeltaAssetOwner, rows)
			seedMailboxDeltaInventory(t, db, mailboxDeltaMailboxOwner, 3)
			before := db.model.snapshot()
			state := &cargoDeltaSQLState{db: db}
			u, sqlDB := mailboxDeltaUnitOfWork(state)
			defer sqlDB.Close()
			record := repository.CloneInventory(seeded)
			err := u.WithinMailboxAssets(ctx, mailboxDeltaAssetOwner, mailboxDeltaMailboxOwner, func(_ repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.MailboxRepository) error {
				bag := inventories.(*characterSettlementScopedInventoryRepository).base.(*mysqlBagStore)
				if !bag.trackSlotChanges {
					return fmt.Errorf("mailbox transaction did not enable slot delta tracking")
				}
				current := record
				if mode == "snapshot_cleared" {
					loaded, found, loadErr := inventories.Load(ctx, mailboxDeltaAssetOwner)
					if loadErr != nil || !found {
						return fmt.Errorf("load asset inventory found=%v err=%w", found, loadErr)
					}
					current = loaded
					bag.loadedSnapshot = nil
				}
				item := current.Slots[changedKey]
				item.Count++
				current.Slots[changedKey] = item
				return repository.SaveInventoryFields(ctx, inventories, current, repository.InventoryFieldSlots)
			})
			if err != nil {
				t.Fatalf("mailbox fallback save: %v", err)
			}
			if state.commits != 1 || state.rollbacks != 0 {
				t.Fatalf("commits=%d rollbacks=%d", state.commits, state.rollbacks)
			}
			wholeContainer := 0
			for _, call := range mailboxDeltaWrites(db.calls) {
				if !strings.HasPrefix(call.Query, "DELETE FROM ") {
					continue
				}
				if !strings.Contains(call.Query, "dnf_inventory_items") && !strings.Contains(call.Query, "dnf_inventory_item_extra") {
					continue
				}
				if strings.Contains(call.Query, "entry_key IN (") {
					t.Fatalf("fallback emitted a slot delta delete: %s", call.Query)
				}
				wholeContainer++
			}
			if wholeContainer != 2 {
				t.Fatalf("fallback rewrote %d collections, want 2", wholeContainer)
			}
			if want, got := mailboxDeltaOwnerLines(before, mailboxDeltaMailboxOwner), mailboxDeltaOwnerLines(db.model.snapshot(), mailboxDeltaMailboxOwner); !reflect.DeepEqual(want, got) {
				t.Fatal("fallback rewrote the mailbox owner's inventory")
			}
			referenceDB := newRowModelDB()
			seedMailboxDeltaInventory(t, referenceDB, mailboxDeltaAssetOwner, rows)
			seedMailboxDeltaInventory(t, referenceDB, mailboxDeltaMailboxOwner, 3)
			reference := repository.CloneInventory(seeded)
			item := reference.Slots[changedKey]
			item.Count++
			reference.Slots[changedKey] = item
			if err := newRowModelGroup(t, referenceDB, false).Inventory.Save(ctx, reference); err != nil {
				t.Fatalf("reference full save: %v", err)
			}
			if !reflect.DeepEqual(referenceDB.model.snapshot(), db.model.snapshot()) {
				t.Fatal("fallback did not match a full rewrite")
			}
		})
	}
}

func TestMySQLMailboxAssetInventoryDeltaRollsBackFailures(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	const rows = 20
	const changedKey = "0:3"
	for _, mode := range []string{"write_failure", "late_abort"} {
		t.Run(mode, func(t *testing.T) {
			db := newRowModelDB()
			seedMailboxDeltaInventory(t, db, mailboxDeltaAssetOwner, rows)
			seedMailboxDeltaInventory(t, db, mailboxDeltaMailboxOwner, 3)
			before := db.model.snapshot()
			state := &cargoDeltaSQLState{db: db}
			if mode == "write_failure" {
				state.failAt = 2
			}
			u, sqlDB := mailboxDeltaUnitOfWork(state)
			defer sqlDB.Close()
			err := u.WithinMailboxAssets(ctx, mailboxDeltaAssetOwner, mailboxDeltaMailboxOwner, func(_ repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.MailboxRepository) error {
				record, found, loadErr := inventories.Load(ctx, mailboxDeltaAssetOwner)
				if loadErr != nil || !found {
					return fmt.Errorf("load asset inventory found=%v err=%w", found, loadErr)
				}
				item := record.Slots[changedKey]
				item.Count++
				record.Slots[changedKey] = item
				if saveErr := repository.SaveInventoryFields(ctx, inventories, record, repository.InventoryFieldSlots); saveErr != nil {
					return saveErr
				}
				if mode == "late_abort" {
					return fmt.Errorf("domain aborted after the slot write")
				}
				return nil
			})
			if err == nil {
				t.Fatal("failed mailbox transaction reported success")
			}
			if mode == "write_failure" && !strings.Contains(err.Error(), "injected cargo write failure") {
				t.Fatalf("unexpected write failure: %v", err)
			}
			if state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("commits=%d rollbacks=%d", state.commits, state.rollbacks)
			}
			if !reflect.DeepEqual(before, db.model.snapshot()) {
				t.Fatal("failed mailbox transaction leaked inventory rows")
			}
		})
	}
}

func TestMySQLMailboxAssetInventoryDeltaKeepsSingleOwnerScope(t *testing.T) {
	t.Setenv("DNFBRIDGE_MYSQL_INSERT_BATCH", "")
	ctx := context.Background()
	db := newRowModelDB()
	seedMailboxDeltaInventory(t, db, mailboxDeltaAssetOwner, 5)
	seedMailboxDeltaInventory(t, db, mailboxDeltaMailboxOwner, 5)
	before := db.model.snapshot()
	state := &cargoDeltaSQLState{db: db}
	u, sqlDB := mailboxDeltaUnitOfWork(state)
	defer sqlDB.Close()
	err := u.WithinMailboxAssets(ctx, mailboxDeltaAssetOwner, mailboxDeltaMailboxOwner, func(_ repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.MailboxRepository) error {
		_, _, loadErr := inventories.Load(ctx, mailboxDeltaMailboxOwner)
		if loadErr == nil {
			return fmt.Errorf("scoped inventory loaded the mailbox owner")
		}
		return loadErr
	})
	if err == nil {
		t.Fatal("cross-owner inventory load was accepted")
	}
	if state.commits != 0 || state.rollbacks != 1 || state.writes != 0 {
		t.Fatalf("commits=%d rollbacks=%d writes=%d", state.commits, state.rollbacks, state.writes)
	}
	if !reflect.DeepEqual(before, db.model.snapshot()) {
		t.Fatal("rejected cross-owner load changed rows")
	}
}
