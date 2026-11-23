package mysql

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestQuestActivePlanningLocksParentBeforeChildren(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	db := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"char-1", now}}, {values: []any{"char-1", now}}}}
	store := &mysqlQuestStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_local"}, writeDBs: []string{"dnf_local"}, tablePrefix: "dnf", lockReads: true, now: time.Now}}}
	_, found, err := store.LoadQuestActivePlanning(context.Background(), "char-1")
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	if len(db.execs) < 2 {
		t.Fatalf("missing queries: %+v", db.execs)
	}
	first := db.execs[0].Query
	if !strings.Contains(first, "`dnf_quests`") || !strings.HasSuffix(first, "FOR UPDATE") {
		t.Fatalf("first lock must be quest parent, got %s", first)
	}
	if !strings.Contains(db.execs[1].Query, "`dnf_quest_states`") {
		t.Fatalf("expected children after parent: %+v", db.execs)
	}
}

func TestQuestActivePlanningMissingParentDoesNotLockOrphanChildren(t *testing.T) {
	db := &fakeSQLDB{}
	store := &mysqlQuestStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_local"}, writeDBs: []string{"dnf_local"}, tablePrefix: "dnf", lockReads: true, now: time.Now}}}
	_, found, err := store.LoadQuestActivePlanning(context.Background(), "missing")
	if err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
	if len(db.execs) != 1 || strings.Contains(db.execs[0].Query, "quest_states") {
		t.Fatalf("orphan child query: %+v", db.execs)
	}
}
