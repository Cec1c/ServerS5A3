package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestMySQLQuestStoreLoadQuestStatus(t *testing.T) {
	const (
		characterID = "character-1"
		questID     = int64(2670)
	)
	parent := fakeSQLRow{values: []any{characterID}}
	missing := fakeSQLRow{err: sql.ErrNoRows}
	statusErr := errors.New("status query failed")

	tests := []struct {
		name       string
		rows       []fakeSQLRow
		wantStatus string
		wantFound  bool
		wantErr    error
		wantGroups []string
	}{
		{
			name:       "states wins even when its status is empty",
			rows:       []fakeSQLRow{parent, {values: []any{""}}, {values: []any{"completed"}}},
			wantStatus: "",
			wantFound:  true,
			wantGroups: []string{"states"},
		},
		{
			name:       "progress falls back only when states has no row",
			rows:       []fakeSQLRow{parent, missing, {values: []any{"cleared"}}},
			wantStatus: "cleared",
			wantFound:  true,
			wantGroups: []string{"states", "progress"},
		},
		{
			name:       "neither group has the quest",
			rows:       []fakeSQLRow{parent, missing, missing},
			wantStatus: "",
			wantFound:  false,
			wantGroups: []string{"states", "progress"},
		},
		{
			name:       "missing parent never reads orphan child rows",
			rows:       []fakeSQLRow{missing, {values: []any{"completed"}}},
			wantStatus: "",
			wantFound:  false,
			wantGroups: nil,
		},
		{
			name:       "states query error is returned without progress fallback",
			rows:       []fakeSQLRow{parent, {err: statusErr}},
			wantStatus: "",
			wantFound:  false,
			wantErr:    statusErr,
			wantGroups: []string{"states"},
		},
		{
			name:       "progress query error is returned",
			rows:       []fakeSQLRow{parent, missing, {err: statusErr}},
			wantStatus: "",
			wantFound:  false,
			wantErr:    statusErr,
			wantGroups: []string{"states", "progress"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeSQLDB{rows: append([]fakeSQLRow(nil), tt.rows...)}
			store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{
				db:          db,
				readDBs:     []string{"dnf_s1_r1"},
				writeDBs:    []string{"dnf_s1_w1"},
				tablePrefix: "dnf",
			}}}

			status, found, err := store.LoadQuestStatus(context.Background(), characterID, questID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("LoadQuestStatus() error = %v, want %v", err, tt.wantErr)
			}
			if status != tt.wantStatus || found != tt.wantFound {
				t.Fatalf("LoadQuestStatus() = (%q, %v), want (%q, %v)", status, found, tt.wantStatus, tt.wantFound)
			}
			if got, want := len(db.execs), 1+len(tt.wantGroups); got != want {
				t.Fatalf("queries = %d, want %d: %+v", got, want, db.execs)
			}
			if got, want := db.execs[0].Query, "SELECT character_id FROM `dnf_s1_r1`.`dnf_quests` WHERE character_id = ?"; got != want {
				t.Fatalf("parent query = %q, want %q", got, want)
			}
			if got := db.execs[0].Args; len(got) != 1 || got[0] != characterID {
				t.Fatalf("parent args = %#v", got)
			}
			for i, group := range tt.wantGroups {
				call := db.execs[i+1]
				if strings.Contains(strings.ToLower(call.Query), "extra") {
					t.Fatalf("status read touched extra data: %s", call.Query)
				}
				if !strings.Contains(call.Query, "`dnf_s1_r1`.`dnf_quest_states`") || !strings.Contains(call.Query, "quest_id = ? LIMIT 1") {
					t.Fatalf("status query is not a single parameterized task read: %s", call.Query)
				}
				if got := call.Args; len(got) != 3 || got[0] != characterID || got[1] != group || got[2] != questID {
					t.Fatalf("status args = %#v, want [%q %q %d]", got, characterID, group, questID)
				}
			}
			for _, call := range db.execs {
				if strings.Contains(strings.ToLower(call.Query), "insert") ||
					strings.Contains(strings.ToLower(call.Query), "update") ||
					strings.Contains(strings.ToLower(call.Query), "delete") {
					t.Fatalf("status read issued a write: %s", call.Query)
				}
			}
		})
	}
}

func TestMySQLQuestStoreLoadQuestStatusUsesTransactionRouter(t *testing.T) {
	const characterID = "character-1"
	db := &fakeSQLDB{rows: []fakeSQLRow{
		{values: []any{characterID}},
		{values: []any{"cleared"}},
	}}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{
		db:          db,
		readDBs:     []string{"dnf_s1_w1"},
		writeDBs:    []string{"dnf_s1_w1"},
		tablePrefix: "dnf",
		lockReads:   true,
	}}}

	status, found, err := store.LoadQuestStatus(context.Background(), characterID, 2670)
	if err != nil || !found || status != "cleared" {
		t.Fatalf("LoadQuestStatus() = (%q, %v, %v)", status, found, err)
	}
	if len(db.execs) != 2 {
		t.Fatalf("queries = %d, want 2", len(db.execs))
	}
	for _, call := range db.execs {
		if !strings.Contains(call.Query, "`dnf_s1_w1`") || !strings.HasSuffix(call.Query, " FOR UPDATE") {
			t.Fatalf("transaction read escaped its shard/lock route: %s", call.Query)
		}
	}
}
