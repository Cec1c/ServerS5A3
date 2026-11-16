package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestPetGrowthDeltaSatietyPreservesHistoryAndArtifacts(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			state := &questDeltaTestDB{}
			if fail {
				state.failAt = 2
			}
			db := sql.OpenDB(questDeltaConnector{state})
			defer db.Close()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			store := &mysqlPetStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: sqlTransactionDB{tx: tx}, lockReads: true, readDBs: []string{"replica"}, writeDBs: []string{"primary"}}}}
			before := repository.PetEntry{PetKey: "p", ItemID: 1, Satiety: 100, SatietyMicros: 100000000, Extra: map[string]string{"keep": "history"}, AppliedClearTokens: map[string]bool{"old": true}, AppliedClearTokenOrder: []string{"old"}}
			after := repository.PetRecord{CharacterID: "c", EquippedKey: "p", UpdatedAt: time.Unix(1800000000, 0), Entries: map[string]repository.PetEntry{"p": before}}
			next := before
			next.Satiety, next.SatietyMicros = 99, 99000000
			after.Entries["p"] = next
			err = store.SavePetGrowth(context.Background(), after, "p", before)
			if fail {
				if err == nil {
					t.Fatal("expected injected error")
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if state.commit != 0 || state.rollback != 1 {
					t.Fatalf("transaction=%+v", state)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if len(state.writes) != 2 {
				t.Fatalf("writes=%+v", state.writes)
			}
			for _, w := range state.writes {
				if !strings.Contains(w.Query, "`primary`") || strings.Contains(w.Query, "DELETE") || strings.Contains(w.Query, "INSERT") || strings.Contains(w.Query, "token") || strings.Contains(w.Query, "extra") || strings.Contains(w.Query, "artifact") {
					t.Fatalf("unrelated write: %+v", w)
				}
			}
		})
	}
}

func TestPetGrowthTokenWindowUsesSingleAppendAndBoundedEviction(t *testing.T) {
	before := repository.PetEntry{AppliedClearTokens: map[string]bool{}}
	for i := 0; i < 128; i++ {
		token := fmt.Sprintf("clear-%03d", i)
		before.AppliedClearTokens[token] = true
		before.AppliedClearTokenOrder = append(before.AppliedClearTokenOrder, token)
	}
	next := repository.ClonePet(repository.PetRecord{Entries: map[string]repository.PetEntry{"p": before}}).Entries["p"]
	delete(next.AppliedClearTokens, "clear-000")
	next.AppliedClearTokens["clear-128"] = true
	next.AppliedClearTokenOrder = append(next.AppliedClearTokenOrder[1:], "clear-128")
	db := &fakeSQLDB{}
	var rows []fakeSQLRow
	for i, token := range before.AppliedClearTokenOrder {
		rows = append(rows, fakeSQLRow{values: []any{i, token}})
	}
	db.rowsets = []fakeSQLRows{{rows: rows}}
	if err := savePetGrowthTokens(context.Background(), db, "tokens", "c", "p", before, next); err != nil {
		t.Fatal(err)
	}
	writes := db.execs[1:]
	if len(writes) != 2 {
		t.Fatalf("expected one delete and one append: %+v", db.execs)
	}
	if !strings.Contains(writes[0].Query, "AND token = ?") || !strings.HasPrefix(writes[1].Query, "INSERT INTO") {
		t.Fatalf("unsafe token window updates: %+v", db.execs)
	}
	if got := writes[1].Args; got[2] != int64(128) || got[3] != "clear-128" {
		t.Fatalf("append=%v", got)
	}
}

func TestPetGrowthDeltaRequiresTransaction(t *testing.T) {
	s := &mysqlPetStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: &fakeSQLDB{}}}}
	if err := s.SavePetGrowth(context.Background(), repository.PetRecord{}, "p", repository.PetEntry{}); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unlocked write err=%v", err)
	}
}

func TestPetGrowthTokenPhysicalOrderCompatibilityAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ordinals []int64
		tokens   []string
		rebuild  bool
		appendAt int64
	}{
		{"zero-based", []int64{0, 1}, []string{"a", "b"}, false, 2},
		{"one-based", []int64{1, 2}, []string{"a", "b"}, false, 3},
		{"sparse", []int64{5, 9}, []string{"a", "b"}, false, 10},
		{"negative-import", []int64{-2, -1}, []string{"a", "b"}, true, 1},
		{"reordered-import", []int64{0, 1}, []string{"b", "a"}, true, 1},
		{"integer-boundary", []int64{math.MaxInt32 - 1, math.MaxInt32}, []string{"a", "b"}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repository.PetEntry{AppliedClearTokens: map[string]bool{"a": true, "b": false}, AppliedClearTokenOrder: []string{"a", "b"}}
			next := repository.PetEntry{AppliedClearTokens: map[string]bool{"b": false, "c": true}, AppliedClearTokenOrder: []string{"b", "c"}}
			rows := []fakeSQLRow{}
			for i, n := range tc.ordinals {
				rows = append(rows, fakeSQLRow{values: []any{n, tc.tokens[i]}})
			}
			db := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: rows}}}
			if err := savePetGrowthTokens(context.Background(), db, "tokens", "c", "p", before, next); err != nil {
				t.Fatal(err)
			}
			writes := db.execs[1:]
			want := 2
			if tc.rebuild {
				want = 3
			}
			if len(writes) != want {
				t.Fatalf("writes=%+v", writes)
			}
			for _, write := range writes {
				if strings.HasPrefix(write.Query, "UPDATE") {
					t.Fatal("survivors renumbered")
				}
			}
			if got := writes[len(writes)-1].Args; got[2] != tc.appendAt || got[3] != "c" {
				t.Fatal(got)
			}
			if tc.rebuild && writes[1].Args[4] != false {
				t.Fatal("legacy false fact lost")
			}
		})
	}
}
