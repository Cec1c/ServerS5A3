package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.CharacterQuestStatusReader = (*mysqlQuestStore)(nil)

func TestMySQLCharacterQuestStatusesUsesOneScopedRealtimeQuery(t *testing.T) {
	db := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		{values: []any{"77", []byte("completed")}},
		{values: []any{"77", []byte("ACTIVE")}},
		{values: []any{"77", []byte("áctive")}},
		{values: []any{"77", []byte("title_book_active")}},
	}}}}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_r1"}, writeDBs: []string{"dnf_w1"}, tablePrefix: "dnf"}}}
	statuses, found, err := store.LoadCharacterQuestStatuses(context.Background(), "77")
	if err != nil || !found || !reflect.DeepEqual(statuses, []string{"completed", "ACTIVE", "áctive", "title_book_active"}) {
		t.Fatalf("statuses=%q found=%t err=%v", statuses, found, err)
	}
	if len(db.execs) != 1 || !reflect.DeepEqual(db.execs[0].Args, []any{"77"}) {
		t.Fatalf("query scope=%+v", db.execs)
	}
	query := db.execs[0].Query
	for _, part := range []string{"`dnf_r1`.`dnf_characters`", "`dnf_r1`.`dnf_quests`", "`dnf_r1`.`dnf_quest_states`", "LEFT JOIN", "s.character_id = q.character_id", "('states', 'progress')", "GROUP BY c.character_id, CAST(s.status AS BINARY)"} {
		if !strings.Contains(query, part) {
			t.Fatalf("query missing %q: %s", part, query)
		}
	}
	if strings.Contains(query, "extra") || strings.Contains(query, "UPDATE") || strings.Contains(query, "LOWER(") {
		t.Fatalf("query reads history/changes status semantics: %s", query)
	}
	// 下一次读取可立即看到接取/交付后的状态，不复用刚才的结论。
	db.rowsets = []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"77", "accepted"}}}}}
	statuses, found, err = store.LoadCharacterQuestStatuses(context.Background(), "77")
	if err != nil || !found || !reflect.DeepEqual(statuses, []string{"accepted"}) || len(db.execs) != 2 {
		t.Fatalf("realtime=%q found=%t err=%v queries=%d", statuses, found, err, len(db.execs))
	}
}

func TestMySQLCharacterQuestStatusesPreservesMissingAndFailures(t *testing.T) {
	boom := errors.New("任务查询失败")
	for _, tc := range []struct {
		name    string
		rows    fakeSQLRows
		found   bool
		failure bool
	}{
		{name: "缺角色"},
		{name: "无任务父行或空状态", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77", nil}}}}, found: true},
		{name: "不同精确角色键", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77 ", "active"}}}}},
		{name: "扫描失败", rows: fakeSQLRows{rows: []fakeSQLRow{{err: boom}}}, failure: true},
		{name: "遍历失败", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77", "completed"}}}, err: boom}, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeSQLDB{rowsets: []fakeSQLRows{tc.rows}}
			store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_r1"}, tablePrefix: "dnf_"}}}
			statuses, found, err := store.LoadCharacterQuestStatuses(context.Background(), "77")
			if found != tc.found || (err != nil) != tc.failure || len(statuses) != 0 {
				t.Fatalf("statuses=%q found=%t err=%v", statuses, found, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := &fakeSQLDB{}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_r1"}}}}
	if _, _, err := store.LoadCharacterQuestStatuses(ctx, "77"); !errors.Is(err, context.Canceled) || len(db.execs) != 0 {
		t.Fatalf("cancel err=%v calls=%d", err, len(db.execs))
	}
}
