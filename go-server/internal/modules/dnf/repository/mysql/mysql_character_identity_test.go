package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func TestCharacterIdentityUsesOneRoutedReadAndPreservesTransactionLock(t *testing.T) {
	for _, locked := range []bool{false, true} {
		db := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"c", "a"}}}}
		characters := newTestMySQLGroup(t, db).Character.(*mysqlCharStore)
		characters.router.lockReads = locked
		identity, found, err := repository.LoadCharacterIdentity(context.Background(), characters, "c")
		if err != nil || !found || identity != (repository.CharacterIdentity{CharacterID: "c", AccountID: "a"}) {
			t.Fatalf("identity=%+v found=%t err=%v", identity, found, err)
		}
		if len(db.execs) != 1 || !strings.Contains(db.execs[0].Query, "SELECT character_id, account_id FROM") || strings.Contains(db.execs[0].Query, "FOR UPDATE") != locked {
			t.Fatalf("identity read lost narrow/lock boundary: %+v", db.execs)
		}
	}
}

func TestCharacterIdentityDoesNotHideReadFailureAsAbsent(t *testing.T) {
	for _, want := range []error{sql.ErrNoRows, errors.New("identity read failed")} {
		db := &fakeSQLDB{rows: []fakeSQLRow{{err: want}}}
		_, found, err := repository.LoadCharacterIdentity(context.Background(), newTestMySQLGroup(t, db).Character, "c")
		if found || (errors.Is(want, sql.ErrNoRows) && err != nil) || (!errors.Is(want, sql.ErrNoRows) && !errors.Is(err, want)) {
			t.Fatalf("found=%t err=%v want=%v", found, err, want)
		}
	}
}
