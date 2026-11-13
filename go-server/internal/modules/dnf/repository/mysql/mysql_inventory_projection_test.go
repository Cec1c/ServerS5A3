package mysql

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestInventorySlotPrefixProjectionNarrowsBothQueriesAndNeverSeedsSnapshot(t *testing.T) {
	f := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"77"}}}, rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{{values: []any{"auction:listing:1", int64(6001), int64(2), false, nil, []byte{1, 2}}}}},
		{rows: []fakeSQLRow{{values: []any{"auction:listing:1", "seller_id", "10"}}}},
	}}
	s := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, writeDBs: []string{"dnf"}, tablePrefix: "dnf"}}, trackSlotChanges: true}
	got, err := s.LoadInventorySlotPrefix(context.Background(), "77", "auction:listing:")
	if err != nil || len(got) != 1 || got["auction:listing:1"].Extra["seller_id"] != "10" || !reflect.DeepEqual(got["auction:listing:1"].RawEntry, []byte{1, 2}) {
		t.Fatalf("projection=%+v err=%v", got, err)
	}
	if s.loadedSnapshot != nil || len(f.execs) != 3 {
		t.Fatalf("partial snapshot/calls: %v %d", s.loadedSnapshot, len(f.execs))
	}
	for _, call := range f.execs[1:] {
		if !strings.Contains(call.Query, "entry_key LIKE ? ESCAPE '!'") || !reflect.DeepEqual(call.Args, []any{"77", "slots", "auction:listing:%"}) {
			t.Fatalf("unbounded query=%+v", call)
		}
	}
	f.rows = []fakeSQLRow{{err: sql.ErrNoRows}}
	f.execs = nil
	if rows, err := s.LoadInventorySlotPrefix(context.Background(), "missing", "x"); err != nil || len(rows) != 0 || len(f.execs) != 1 {
		t.Fatalf("missing owner=%v %v calls=%d", rows, err, len(f.execs))
	}
	f.rows = []fakeSQLRow{{values: []any{"77"}}}
	f.execs = nil
	if _, err := s.LoadInventorySlotPrefix(context.Background(), "77", "x!_%"); err != nil {
		t.Fatal(err)
	}
	if f.execs[1].Args[2] != "x!!!_!%%" {
		t.Fatalf("wildcard not escaped: %v", f.execs[1].Args)
	}
}

func TestCharacterTradeDeltaKeepsSeparateOwnerImagesAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db := newRowModelDB()
			seedMailboxDeltaInventory(t, db, "77", 40)
			seedMailboxDeltaInventory(t, db, "88", 40)
			before := db.model.snapshot()
			state := &cargoDeltaSQLState{db: db}
			mailbox, sqlDB := mailboxDeltaUnitOfWork(state)
			defer sqlDB.Close()
			u := &mysqlCharacterTradeUnitOfWork{router: mailbox.router}
			failure := errors.New("late transaction rejection")
			err := u.WithinCharacterTrade(context.Background(), "77", "88", func(_ repository.CharacterRepository, inventories repository.InventoryRepository) error {
				a, found, err := inventories.Load(context.Background(), "77")
				if err != nil || !found {
					return errors.New("first inventory missing")
				}
				b, found, err := inventories.Load(context.Background(), "88")
				if err != nil || !found {
					return errors.New("second inventory missing")
				}
				item := a.Slots["0:1"]
				item.Count--
				a.Slots["0:1"] = item
				item = b.Slots["0:2"]
				item.Count++
				b.Slots["0:2"] = item
				if err := repository.SaveInventoryFields(context.Background(), inventories, a, repository.InventoryFieldSlots); err != nil {
					return err
				}
				if err := repository.SaveInventoryFields(context.Background(), inventories, b, repository.InventoryFieldSlots); err != nil {
					return err
				}
				item = a.Slots["0:1"]
				item.Count--
				a.Slots["0:1"] = item
				if err := repository.SaveInventoryFields(context.Background(), inventories, a, repository.InventoryFieldSlots); err != nil {
					return err
				}
				if fail {
					return failure
				}
				return nil
			})
			if fail {
				if !errors.Is(err, failure) || state.commits != 0 || state.rollbacks != 1 || !reflect.DeepEqual(before, db.model.snapshot()) {
					t.Fatalf("rollback err=%v commits=%d rollbacks=%d", err, state.commits, state.rollbacks)
				}
				return
			}
			if err != nil || state.commits != 1 {
				t.Fatalf("commit=%d err=%v", state.commits, err)
			}
			deletes := 0
			for _, call := range db.calls {
				if strings.HasPrefix(call.Query, "DELETE FROM") && strings.Contains(call.Query, "inventory_") {
					deletes++
					if !strings.Contains(call.Query, "entry_key IN") || len(call.Args) != 3 {
						t.Fatalf("whole owner overwritten: %+v", call)
					}
				}
			}
			if deletes != 6 {
				t.Fatalf("changed-key deletes=%d want6", deletes)
			}
			for _, id := range []string{"77", "88"} {
				key := map[string]string{"77": "0:1", "88": "0:2"}[id]
				if !reflect.DeepEqual(mailboxDeltaWithoutEntry(mailboxDeltaOwnerLines(before, id), key), mailboxDeltaWithoutEntry(mailboxDeltaOwnerLines(db.model.snapshot(), id), key)) {
					t.Fatalf("untouched owner rows changed: %s", id)
				}
			}
		})
	}
}
