package mysql

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
)

var _ repository.CharacterSelectionIdentityReader = (*mysqlCharStore)(nil)

const characterSelectionIdentitySelect = "SELECT character_id, account_id, slot FROM `dnf_s1_r1`.`dnf_characters` WHERE account_id = ? AND delete_flag = 0 ORDER BY slot ASC, character_id ASC LIMIT ?"

func newCharacterSelectionIdentityStore(t *testing.T, database *fakeSQLDB) *mysqlCharStore {
	t.Helper()
	characters, ok := newTestMySQLGroup(t, database).Character.(*mysqlCharStore)
	if !ok {
		t.Fatal("MySQL character repository is not a mysqlCharStore")
	}
	return characters
}

func characterSelectionIdentityRows(count int) []fakeSQLRow {
	rows := make([]fakeSQLRow, 0, count)
	for slot := 0; slot < count; slot++ {
		rows = append(rows, fakeSQLRow{values: []any{"character-" + strconv.Itoa(slot), "dnf:1", slot}})
	}
	return rows
}

func TestCharacterSelectionIdentityUsesNarrowAccountQuery(t *testing.T) {
	database := &fakeSQLDB{rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{{
			values: []any{"character-7", "dnf:1", 7},
		}}},
	}}
	characters := newCharacterSelectionIdentityStore(t, database)

	records, err := characters.ListCharacterIdentities(context.Background(), "  dnf:1\t", 9)
	if err != nil {
		t.Fatalf("ListCharacterIdentities() error = %v", err)
	}
	if len(records) != 1 || records[0].CharacterID != "character-7" || records[0].AccountID != "dnf:1" || records[0].Slot != 7 {
		t.Fatalf("ListCharacterIdentities() = %+v", records)
	}

	call := requireOneExec(t, database)
	if call.Query != characterSelectionIdentitySelect {
		t.Fatalf("query = %q, want %q", call.Query, characterSelectionIdentitySelect)
	}
	if strings.Count(call.Query, " FROM ") != 1 || strings.Contains(call.Query, " JOIN ") {
		t.Fatalf("identity query must stay on the character table only: %q", call.Query)
	}
	if !reflect.DeepEqual(call.Args, []any{"dnf:1", 9}) {
		t.Fatalf("query args = %#v, want trimmed account and requested limit", call.Args)
	}
}

func TestCharacterSelectionIdentityOneQueryForSupportedRowCounts(t *testing.T) {
	tests := []struct {
		name            string
		configuredLimit int
		rows            int
		queryLimit      int
	}{
		{name: "one", configuredLimit: 1, rows: 1, queryLimit: 1},
		{name: "sixteen", configuredLimit: 16, rows: 16, queryLimit: 16},
		{name: "seventeen", configuredLimit: 17, rows: 17, queryLimit: 17},
		{name: "default_thirty_two", configuredLimit: 0, rows: 32, queryLimit: repository.DefaultCharacterSlotLimit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: characterSelectionIdentityRows(test.rows)}}}
			characters := newCharacterSelectionIdentityStore(t, database)

			records, err := characters.ListCharacterIdentities(context.Background(), "dnf:1", test.configuredLimit)
			if err != nil {
				t.Fatalf("ListCharacterIdentities() error = %v", err)
			}
			if len(records) != test.rows {
				t.Fatalf("record count = %d, want %d", len(records), test.rows)
			}
			call := requireOneExec(t, database)
			if !reflect.DeepEqual(call.Args, []any{"dnf:1", test.queryLimit}) {
				t.Fatalf("query args = %#v, want limit %d", call.Args, test.queryLimit)
			}
		})
	}
}

func TestCharacterSelectionIdentityPreservesPerShardRemainingLimit(t *testing.T) {
	database := &fakeSQLDB{rowsets: []fakeSQLRows{
		{rows: characterSelectionIdentityRows(2)},
		{rows: characterSelectionIdentityRows(1)},
	}}
	characters := newCharacterSelectionIdentityStore(t, database)
	characters.router.readDBs = []string{"dnf_s1_r1", "dnf_s1_r2"}

	records, err := characters.ListCharacterIdentities(context.Background(), "dnf:1", 3)
	if err != nil {
		t.Fatalf("ListCharacterIdentities() error = %v", err)
	}
	if len(records) != 3 || len(database.execs) != 2 {
		t.Fatalf("records=%d queries=%d", len(records), len(database.execs))
	}
	if !strings.Contains(database.execs[0].Query, "`dnf_s1_r1`.`dnf_characters`") ||
		!strings.Contains(database.execs[1].Query, "`dnf_s1_r2`.`dnf_characters`") {
		t.Fatalf("shard routing changed: %v", execQueries(database))
	}
	if !reflect.DeepEqual(database.execs[0].Args, []any{"dnf:1", 3}) ||
		!reflect.DeepEqual(database.execs[1].Args, []any{"dnf:1", 1}) {
		t.Fatalf("remaining shard limits = %#v / %#v", database.execs[0].Args, database.execs[1].Args)
	}
}

func TestCharacterSelectionIdentityStopsAfterShardFillsLimit(t *testing.T) {
	database := &fakeSQLDB{rowsets: []fakeSQLRows{
		{rows: characterSelectionIdentityRows(3)},
		{rows: characterSelectionIdentityRows(1)},
	}}
	characters := newCharacterSelectionIdentityStore(t, database)
	characters.router.readDBs = []string{"dnf_s1_r1", "dnf_s1_r2"}

	records, err := characters.ListCharacterIdentities(context.Background(), "dnf:1", 3)
	if err != nil {
		t.Fatalf("ListCharacterIdentities() error = %v", err)
	}
	if len(records) != 3 || len(database.execs) != 1 {
		t.Fatalf("records=%d queries=%d, want 3/1", len(records), len(database.execs))
	}
	if !reflect.DeepEqual(database.execs[0].Args, []any{"dnf:1", 3}) {
		t.Fatalf("query args = %#v", database.execs[0].Args)
	}
}

func TestCharacterSelectionIdentityLockReadsUsesForUpdate(t *testing.T) {
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: characterSelectionIdentityRows(1)}}}
	characters := newCharacterSelectionIdentityStore(t, database)
	characters.router.lockReads = true

	if _, err := characters.ListCharacterIdentities(context.Background(), "dnf:1", 1); err != nil {
		t.Fatalf("ListCharacterIdentities() error = %v", err)
	}
	call := requireOneExec(t, database)
	if call.Query != characterSelectionIdentitySelect+" FOR UPDATE" {
		t.Fatalf("locked query = %q", call.Query)
	}
}

func TestCharacterSelectionIdentityRejectsEmptyAccountWithoutQuery(t *testing.T) {
	for _, account := range []string{"", " \t\r\n"} {
		database := &fakeSQLDB{}
		characters := newCharacterSelectionIdentityStore(t, database)

		records, err := characters.ListCharacterIdentities(context.Background(), account, 1)
		if !errors.Is(err, platformdb.ErrRecordKeyRequired) {
			t.Fatalf("account=%q error=%v, want record key required", account, err)
		}
		if records != nil || len(database.execs) != 0 {
			t.Fatalf("account=%q records=%v queries=%v", account, records, execQueries(database))
		}
	}
}

func TestCharacterSelectionIdentityPropagatesScanAndRowsErrors(t *testing.T) {
	failure := errors.New("identity read failed")
	tests := []struct {
		name string
		rows fakeSQLRows
	}{
		{
			name: "scan",
			rows: fakeSQLRows{rows: []fakeSQLRow{
				{values: []any{"character-0", "dnf:1", 0}},
				{err: failure},
			}},
		},
		{
			name: "rows",
			rows: fakeSQLRows{
				rows: []fakeSQLRow{{values: []any{"character-0", "dnf:1", 0}}},
				err:  failure,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeSQLDB{rowsets: []fakeSQLRows{test.rows}}
			characters := newCharacterSelectionIdentityStore(t, database)

			records, err := characters.ListCharacterIdentities(context.Background(), "dnf:1", 32)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %v", err, failure)
			}
			if records != nil {
				t.Fatalf("partial records returned on failure: %+v", records)
			}
			if len(database.execs) != 1 {
				t.Fatalf("queries = %v, want exactly one", execQueries(database))
			}
		})
	}
}
