package mysql

import (
	"context"
	"strings"
	"testing"
)

func TestP0SettingsJoinedLoadOmitsOrderByFilesort(t *testing.T) {
	t.Run("unlocked_join_omits_order_by", func(t *testing.T) {
		db := &fakeSQLDB{
			rowsets: []fakeSQLRows{
				{
					rows: []fakeSQLRow{
						{values: []any{"character:17:buff_swap_native", nil, "z_key", "val_z"}},
						{values: []any{"character:17:buff_swap_native", nil, "a_key", "val_a"}},
					},
				},
			},
		}
		store := newTestMySQLGroup(t, db).Settings
		got, found, err := store.Load(context.Background(), "character:17:buff_swap_native")
		if err != nil || !found {
			t.Fatalf("Load failed: found=%v err=%v", found, err)
		}
		if len(db.execs) != 1 {
			t.Fatalf("expected 1 joined query, got %d: %+v", len(db.execs), db.execs)
		}
		queryUpper := strings.ToUpper(db.execs[0].Query)
		if !strings.Contains(queryUpper, "LEFT JOIN") {
			t.Fatalf("expected LEFT JOIN query, got: %s", db.execs[0].Query)
		}
		if strings.Contains(queryUpper, "ORDER BY") {
			t.Fatalf("unlocked loadJoinedSettings must omit ORDER BY to avoid MariaDB disk temp table on LONGTEXT: %s", db.execs[0].Query)
		}
		if got.Values["a_key"] != "val_a" || got.Values["z_key"] != "val_z" || len(got.Values) != 2 {
			t.Fatalf("unexpected values map: %+v", got.Values)
		}
	})

	t.Run("locked_read_keeps_deterministic_pk_order", func(t *testing.T) {
		db := &fakeSQLDB{
			rows: []fakeSQLRow{{values: []any{"s", nil}}},
			rowsets: []fakeSQLRows{
				{rows: []fakeSQLRow{{values: []any{"a", "1"}}}},
			},
		}
		store := newTestMySQLGroup(t, db).Settings.(*mysqlSettingsStore)
		store.router.lockReads = true
		got, found, err := store.Load(context.Background(), "s")
		if err != nil || !found || got.Values["a"] != "1" {
			t.Fatalf("locked Load failed: got=%+v found=%v err=%v", got, found, err)
		}
		if len(db.execs) != 2 {
			t.Fatalf("expected 2 locked queries, got %d", len(db.execs))
		}
		if !strings.Contains(strings.ToUpper(db.execs[0].Query), "FOR UPDATE") ||
			!strings.Contains(strings.ToUpper(db.execs[1].Query), "ORDER BY ENTRY_KEY") {
			t.Fatalf("locked read path unexpectedly changed: %+v", db.execs)
		}
	})
}
