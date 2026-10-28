package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.CharacterQuestActivityReader = (*mysqlQuestStore)(nil)

func TestMySQLQuestActivitiesReadsOnlyRealtimeIDsAndRawStatuses(t *testing.T) {
	db := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		{values: []any{"77", int64(2683), []byte(" ACTIVE\t")}},
		{values: []any{"77", int64(4874), []byte("áctive")}},
		{values: []any{"77", int64(8643), "accepted"}},
	}}}}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_r1"}, writeDBs: []string{"dnf_w1"}, tablePrefix: "dnf"}}}
	got, found, err := store.LoadCharacterQuestActivities(context.Background(), "77")
	want := []repository.QuestActivity{{QuestID: 2683, Status: " ACTIVE\t"}, {QuestID: 4874, Status: "áctive"}, {QuestID: 8643, Status: "accepted"}}
	if err != nil || !found || !reflect.DeepEqual(got, want) || len(db.execs) != 1 || !reflect.DeepEqual(db.execs[0].Args, []any{"77"}) {
		t.Fatalf("got=%+v found=%t err=%v calls=%+v", got, found, err, db.execs)
	}
	query := db.execs[0].Query
	for _, part := range []string{"`dnf_r1`.`dnf_characters`", "`dnf_r1`.`dnf_quests`", "`dnf_r1`.`dnf_quest_states`", "s.character_id = q.character_id", "('states', 'progress')", "s.quest_id", "CAST(s.status AS BINARY) <> CAST('completed' AS BINARY)"} {
		if !strings.Contains(query, part) {
			t.Fatalf("查询缺少%q：%s", part, query)
		}
	}
	for _, part := range []string{"extra", "progress_value", "UPDATE", "LOWER(", "GROUP BY"} {
		if strings.Contains(query, part) {
			t.Fatalf("查询扩大范围或合并原始状态：%s", query)
		}
	}
	db.rowsets = []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"77", int64(1), "completed"}}}}}
	got, _, err = store.LoadCharacterQuestActivities(context.Background(), "77")
	if err != nil || len(got) != 1 || got[0].QuestID != 1 || got[0].Status != "completed" || len(db.execs) != 2 {
		t.Fatal("接取/完成后的状态没有实时重读")
	}
}

func TestMySQLQuestActivitiesPreservesMissingAndFailures(t *testing.T) {
	boom := errors.New("任务查询失败")
	for _, tc := range []struct {
		name           string
		rows           fakeSQLRows
		found, failure bool
	}{
		{name: "缺角色"},
		{name: "缺父行/无任务", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77", nil, nil}}}}, found: true},
		{name: "角色键不精确", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77 ", int64(1), "active"}}}}},
		{name: "扫描失败", rows: fakeSQLRows{rows: []fakeSQLRow{{err: boom}}}, failure: true},
		{name: "遍历失败", rows: fakeSQLRows{rows: []fakeSQLRow{{values: []any{"77", int64(1), "active"}}}, err: boom}, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeSQLDB{rowsets: []fakeSQLRows{tc.rows}}
			store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"dnf_r1"}}}}
			got, found, err := store.LoadCharacterQuestActivities(context.Background(), "77")
			if found != tc.found || (err != nil) != tc.failure || len(got) != 0 {
				t.Fatalf("got=%+v found=%t err=%v", got, found, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := &fakeSQLDB{}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db}}}
	if _, _, err := store.LoadCharacterQuestActivities(ctx, "77"); !errors.Is(err, context.Canceled) || len(db.execs) != 0 {
		t.Fatalf("取消没有阻止查询：%v", err)
	}
}
