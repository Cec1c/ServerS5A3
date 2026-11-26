package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestP5InventoryItemProjectionsOmitOrderByFilesort(t *testing.T) {
	t.Run("LoadInventorySlotItemPrefix_no_order_by_and_interleaved_extras", func(t *testing.T) {
		raw1 := []byte{1, 2, 3}
		raw2 := []byte{9, 8}
		f := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
			// Deliberately unsorted & interleaved across entry_key and extra_key.
			{values: []any{"slot:20", int64(6001), int64(5), true, nil, raw2, "z_attr", "zv"}},
			{values: []any{"slot:10", int64(6001), int64(2), false, nil, raw1, "b_attr", "bv"}},
			{values: []any{"slot:20", int64(6001), int64(5), true, nil, raw2, "a_attr", "av"}},
			{values: []any{"slot:10", int64(6001), int64(2), false, nil, raw1, "a_attr", "a1"}},
			{values: []any{"slot:30", int64(6001), int64(1), false, nil, nil, nil, nil}},
		}}}}
		before := &repository.InventoryRecord{CharacterID: "keep", Slots: map[string]repository.ItemStack{"x": {ItemID: 1}}}
		s := &mysqlBagStore{
			mysqlStoreBase:   mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf"}},
			trackSlotChanges: true,
			loadedSnapshot:   before,
		}
		got, err := s.LoadInventorySlotItemPrefix(context.Background(), "char-1", "slot:", 6001)
		if err != nil {
			t.Fatalf("LoadInventorySlotItemPrefix failed: %v", err)
		}
		if len(f.execs) != 1 {
			t.Fatalf("expected 1 query, got %d", len(f.execs))
		}
		qUpper := strings.ToUpper(f.execs[0].Query)
		if strings.Contains(qUpper, "ORDER BY") {
			t.Fatalf("LoadInventorySlotItemPrefix must omit ORDER BY to avoid MariaDB disk temp table on LONGBLOB/LONGTEXT: %s", f.execs[0].Query)
		}
		if s.loadedSnapshot != before {
			t.Fatal("partial projection modified loadedSnapshot")
		}
		if len(got) != 3 {
			t.Fatalf("got %d stacks, want 3: %+v", len(got), got)
		}
		if got["slot:10"].Extra["a_attr"] != "a1" || got["slot:10"].Extra["b_attr"] != "bv" || !reflect.DeepEqual(got["slot:10"].RawEntry, []byte{1, 2, 3}) {
			t.Fatalf("slot:10 mismatch: %+v", got["slot:10"])
		}
		if got["slot:20"].Extra["a_attr"] != "av" || got["slot:20"].Extra["z_attr"] != "zv" || !got["slot:20"].Bind {
			t.Fatalf("slot:20 mismatch: %+v", got["slot:20"])
		}
		if got["slot:30"].RawEntry != nil || len(got["slot:30"].Extra) != 0 {
			t.Fatalf("slot:30 null raw/extra mismatch: %+v", got["slot:30"])
		}
	})

	t.Run("LoadInventorySlotItemPrefixFound_no_order_by_empty_and_escaped_prefix", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			prefix  string
			wantArg string
		}{
			{name: "empty_prefix", prefix: "", wantArg: ""},
			{name: "escaped_prefix", prefix: "a!_%:", wantArg: "a!!!_!%:%"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := []byte{7, 6}
				f := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
					{values: []any{"char-2", tc.prefix + "2", int64(3330), int64(3), false, nil, raw, "k2", "v2"}},
					{values: []any{"char-2", tc.prefix + "1", int64(3330), int64(1), true, nil, nil, nil, nil}},
					{values: []any{"char-2", tc.prefix + "2", int64(3330), int64(3), false, nil, raw, "k1", "v1"}},
				}}}}
				before := &repository.InventoryRecord{CharacterID: "keep"}
				s := &mysqlBagStore{
					mysqlStoreBase:   mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf"}},
					trackSlotChanges: true,
					loadedSnapshot:   before,
				}
				got, found, err := s.LoadInventorySlotItemPrefixFound(context.Background(), "char-2", tc.prefix, 3330)
				if err != nil || !found {
					t.Fatalf("LoadInventorySlotItemPrefixFound failed: found=%v err=%v", found, err)
				}
				if len(f.execs) != 1 {
					t.Fatalf("expected 1 query, got %d", len(f.execs))
				}
				qUpper := strings.ToUpper(f.execs[0].Query)
				if strings.Contains(qUpper, "ORDER BY") {
					t.Fatalf("LoadInventorySlotItemPrefixFound must omit ORDER BY to avoid MariaDB disk temp table on LONGBLOB/LONGTEXT: %s", f.execs[0].Query)
				}
				if tc.prefix != "" {
					if len(f.execs[0].Args) != 4 || f.execs[0].Args[2] != tc.wantArg {
						t.Fatalf("unexpected escaped args: %+v", f.execs[0].Args)
					}
				} else if len(f.execs[0].Args) != 3 {
					t.Fatalf("unexpected empty-prefix args: %+v", f.execs[0].Args)
				}
				if s.loadedSnapshot != before {
					t.Fatal("partial projection modified loadedSnapshot")
				}
				if len(got) != 2 || got[tc.prefix+"2"].Extra["k1"] != "v1" || got[tc.prefix+"2"].Extra["k2"] != "v2" || !got[tc.prefix+"1"].Bind {
					t.Fatalf("unexpected stacks: %+v", got)
				}
			})
		}
	})

	t.Run("LoadInventorySlotItemPrefixFound_absent_vs_present_empty", func(t *testing.T) {
		fAbsent := &fakeSQLDB{rowsets: []fakeSQLRows{{}}}
		sAbsent := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: fAbsent, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}}
		got, found, err := sAbsent.LoadInventorySlotItemPrefixFound(context.Background(), "missing", "", 3330)
		if err != nil || found || len(got) != 0 {
			t.Fatalf("absent owner: got=%+v found=%v err=%v", got, found, err)
		}

		fEmpty := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
			{values: []any{"present", nil, nil, nil, nil, nil, nil, nil, nil}},
		}}}}
		sEmpty := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: fEmpty, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}}
		got, found, err = sEmpty.LoadInventorySlotItemPrefixFound(context.Background(), "present", "", 3330)
		if err != nil || !found || len(got) != 0 {
			t.Fatalf("present empty owner: got=%+v found=%v err=%v", got, found, err)
		}
	})
}

func TestP5CharacterExtraStatsNarrowRead(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	t.Run("single_left_join_exact_case_and_no_order_by", func(t *testing.T) {
		f := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
			{values: []any{"101", "acc-1", 1, "Hero", "slayer", 90, now, now, "event_new_semester_v1:20261120T220000Z:day_bitmap", int64(255)}},
			// Simulate MariaDB utf8mb4_unicode_ci returning a case-mismatched key.
			{values: []any{"101", "acc-1", 1, "Hero", "slayer", 90, now, now, "EVENT_NEW_SEMESTER_V1:20261120T220000Z:QUALIFIED_DAYS", int64(99)}},
			{values: []any{"101", "acc-1", 1, "Hero", "slayer", 90, now, now, "event_new_semester_v1:20261120T220000Z:version", int64(1)}},
		}}}}
		store := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
			db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf",
			env:          mysqlStoreEnvironment{captured: true, mirrorStatsAuthoritative: true},
			sqlTemplates: &mysqlSQLTemplates{},
		}}}
		got, found, err := store.LoadCharacterExtraStats(
			context.Background(),
			"101",
			"event_new_semester_v1:20261120T220000Z:day_bitmap",
			"event_new_semester_v1:20261120T220000Z:qualified_days",
			"event_new_semester_v1:20261120T220000Z:version",
		)
		if err != nil || !found {
			t.Fatalf("LoadCharacterExtraStats failed: found=%v err=%v", found, err)
		}
		if len(f.execs) != 1 {
			t.Fatalf("expected 1 SQL query (no tx, no full Load), got %d: %+v", len(f.execs), f.execs)
		}
		qUpper := strings.ToUpper(f.execs[0].Query)
		if !strings.Contains(qUpper, "LEFT JOIN") || !strings.Contains(qUpper, "ON S.CHARACTER_ID = C.CHARACTER_ID AND S.STAT_KEY IN (?,?,?)") {
			t.Fatalf("expected LEFT JOIN with ON filter, got: %s", f.execs[0].Query)
		}
		if strings.Contains(qUpper, "ORDER BY") {
			t.Fatalf("unexpected ORDER BY: %s", f.execs[0].Query)
		}
		if got.CharacterID != "101" || got.AccountID != "acc-1" || got.Level != 90 {
			t.Fatalf("base fields mismatch: %+v", got)
		}
		if got.Stats["event_new_semester_v1:20261120T220000Z:day_bitmap"] != 255 || got.Stats["event_new_semester_v1:20261120T220000Z:version"] != 1 {
			t.Fatalf("expected exact-case stats missing: %+v", got.Stats)
		}
		if _, leaked := got.Stats["EVENT_NEW_SEMESTER_V1:20261120T220000Z:QUALIFIED_DAYS"]; leaked {
			t.Fatalf("case-mismatched key leaked into Stats: %+v", got.Stats)
		}
		if _, leaked := got.Stats["event_new_semester_v1:20261120T220000Z:qualified_days"]; leaked {
			t.Fatalf("unmatched lowercase key unexpectedly populated: %+v", got.Stats)
		}
	})

	t.Run("missing_character_vs_present_with_all_keys_missing", func(t *testing.T) {
		fMissing := &fakeSQLDB{rowsets: []fakeSQLRows{{}}}
		sMissing := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: fMissing, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}}
		_, found, err := sMissing.LoadCharacterExtraStats(context.Background(), "404", "event_k:1")
		if err != nil || found {
			t.Fatalf("missing character: found=%v err=%v", found, err)
		}

		fPresent := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
			{values: []any{"102", "acc-2", 0, "Empty", "mage", 17, now, now, nil, nil}},
		}}}}
		sPresent := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: fPresent, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}}
		got, found, err := sPresent.LoadCharacterExtraStats(context.Background(), "102", "event_k:1", "event_k:2")
		if err != nil || !found || got.CharacterID != "102" || got.Level != 17 || len(got.Stats) != 0 {
			t.Fatalf("present with zero keys: got=%+v found=%v err=%v", got, found, err)
		}
	})

	t.Run("canceled_ctx_skips_db", func(t *testing.T) {
		f := &fakeSQLDB{}
		s := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, found, err := s.LoadCharacterExtraStats(ctx, "102", "event_k:1")
		if !errors.Is(err, context.Canceled) || found || len(f.execs) != 0 {
			t.Fatalf("canceled ctx: found=%v err=%v execs=%d", found, err, len(f.execs))
		}
	})

	t.Run("zero_keys_uses_mirror_base_query", func(t *testing.T) {
		f := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"103", "acc-3", 2, "Base", "gunner", 85, now, now}}}}
		s := &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
			db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf",
			env:          mysqlStoreEnvironment{captured: true, mirrorStatsAuthoritative: true},
			sqlTemplates: &mysqlSQLTemplates{},
		}}}
		got, found, err := s.LoadCharacterExtraStats(context.Background(), "103")
		if err != nil || !found || got.Level != 85 || len(f.execs) != 1 || strings.Contains(strings.ToUpper(f.execs[0].Query), "JOIN") {
			t.Fatalf("zero keys base query mismatch: got=%+v found=%v err=%v execs=%+v", got, found, err, f.execs)
		}
	})
}
