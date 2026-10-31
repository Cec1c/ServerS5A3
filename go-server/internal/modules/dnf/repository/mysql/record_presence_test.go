package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

type recordPresenceCase struct {
	name        string
	tableSuffix string
	keyColumn   string
	key         string
	newStore    func(mysqlRouter) repository.RecordPresenceReader
}

func recordPresenceCases() []recordPresenceCase {
	return []recordPresenceCase{
		{
			name:        "inventory",
			tableSuffix: mysqlBagTable,
			keyColumn:   "character_id",
			key:         "character-presence",
			newStore: func(router mysqlRouter) repository.RecordPresenceReader {
				return &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: router}}
			},
		},
		{
			name:        "equipment",
			tableSuffix: mysqlEquipmentTable,
			keyColumn:   "character_id",
			key:         "character-presence",
			newStore: func(router mysqlRouter) repository.RecordPresenceReader {
				return &mysqlEquipmentStore{mysqlStoreBase: mysqlStoreBase{router: router}}
			},
		},
		{
			name:        "skill",
			tableSuffix: mysqlSkillTable,
			keyColumn:   "character_id",
			key:         "character-presence",
			newStore: func(router mysqlRouter) repository.RecordPresenceReader {
				return &mysqlSkillStore{mysqlStoreBase: mysqlStoreBase{router: router}}
			},
		},
		{
			name:        "settings",
			tableSuffix: mysqlSettingsTable,
			keyColumn:   "scope",
			key:         "character-presence",
			newStore: func(router mysqlRouter) repository.RecordPresenceReader {
				return &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: router}}
			},
		},
	}
}

func TestRecordPresenceExistsReadsOnlyRoutedParent(t *testing.T) {
	readDBs := []string{"presence_read_a", "presence_read_b"}
	for _, tc := range recordPresenceCases() {
		for _, locked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/locked=%t", tc.name, locked), func(t *testing.T) {
				database := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{1}}}}
				router := mysqlRouter{
					db:          database,
					readDBs:     readDBs,
					writeDBs:    []string{"presence_write"},
					tablePrefix: "dnf",
					lockReads:   locked,
				}

				exists, err := tc.newStore(router).Exists(context.Background(), tc.key)
				if err != nil || !exists {
					t.Fatalf("Exists() exists=%t err=%v", exists, err)
				}
				if len(database.execs) != 1 {
					t.Fatalf("parent existence issued %d queries/writes: %+v", len(database.execs), database.execs)
				}
				readDB := readDBs[pickDatabase(tc.key, len(readDBs))]
				wantQuery := "SELECT 1 FROM " + mysqlTable(readDB, "dnf", tc.tableSuffix) + " WHERE " + tc.keyColumn + " = ?"
				if locked {
					wantQuery += " FOR UPDATE"
				}
				call := database.execs[0]
				if call.Query != wantQuery {
					t.Fatalf("query = %q, want %q", call.Query, wantQuery)
				}
				if len(call.Args) != 1 || call.Args[0] != tc.key {
					t.Fatalf("args = %#v, want [%q]", call.Args, tc.key)
				}
				if strings.Contains(call.Query, "presence_write") {
					t.Fatalf("query used write database: %q", call.Query)
				}
			})
		}
	}
}

func TestRecordPresenceExistsTreatsNoRowsAsAbsent(t *testing.T) {
	for _, tc := range recordPresenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			database := &fakeSQLDB{}
			exists, err := tc.newStore(recordPresenceTestRouter(database)).Exists(context.Background(), tc.key)
			if err != nil || exists {
				t.Fatalf("Exists() exists=%t err=%v", exists, err)
			}
			if len(database.execs) != 1 || !strings.Contains(database.execs[0].Query, "SELECT 1 FROM") {
				t.Fatalf("unexpected parent read: %+v", database.execs)
			}
		})
	}
}

func TestRecordPresenceExistsReturnsReadErrors(t *testing.T) {
	wantErr := errors.New("presence read failed")
	for _, tc := range recordPresenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			database := &fakeSQLDB{rows: []fakeSQLRow{{err: wantErr}}}
			exists, err := tc.newStore(recordPresenceTestRouter(database)).Exists(context.Background(), tc.key)
			if exists || !errors.Is(err, wantErr) {
				t.Fatalf("Exists() exists=%t err=%v, want %v", exists, err, wantErr)
			}
		})
	}
}

func TestRecordPresenceExistsReturnsRouterErrors(t *testing.T) {
	for _, tc := range recordPresenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			database := &fakeSQLDB{}
			exists, err := tc.newStore(recordPresenceTestRouter(database)).Exists(context.Background(), "  ")
			if exists || err == nil {
				t.Fatalf("Exists() exists=%t err=%v, want router error", exists, err)
			}
			if len(database.execs) != 0 {
				t.Fatalf("router failure reached SQL: %+v", database.execs)
			}
		})
	}
}

func recordPresenceTestRouter(database SQLDB) mysqlRouter {
	return mysqlRouter{
		db:          database,
		readDBs:     []string{"presence_read"},
		writeDBs:    []string{"presence_write"},
		tablePrefix: "dnf",
	}
}
