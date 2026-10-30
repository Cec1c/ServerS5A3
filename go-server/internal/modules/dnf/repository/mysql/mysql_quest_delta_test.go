package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

type questDeltaTestDB struct {
	old                      repository.QuestState
	queries                  []string
	writes                   []fakeSQLCall
	commit, rollback, failAt int
}
type questDeltaConnector struct{ state *questDeltaTestDB }

func (c questDeltaConnector) Connect(context.Context) (driver.Conn, error) {
	return &questDeltaConn{state: c.state}, nil
}
func (c questDeltaConnector) Driver() driver.Driver { return questDeltaDriver{c.state} }

type questDeltaDriver struct{ state *questDeltaTestDB }

func (d questDeltaDriver) Open(string) (driver.Conn, error) {
	return &questDeltaConn{state: d.state}, nil
}

type questDeltaConn struct{ state *questDeltaTestDB }

func (*questDeltaConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*questDeltaConn) Close() error                { return nil }
func (c *questDeltaConn) Begin() (driver.Tx, error) { return questDeltaTx{c.state}, nil }
func (c *questDeltaConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

type questDeltaTx struct{ state *questDeltaTestDB }

func (t questDeltaTx) Commit() error   { t.state.commit++; return nil }
func (t questDeltaTx) Rollback() error { t.state.rollback++; return nil }
func (c *questDeltaConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	a := make([]any, len(args))
	for i, v := range args {
		a[i] = v.Value
	}
	c.state.writes = append(c.state.writes, fakeSQLCall{q, a})
	if c.state.failAt == len(c.state.writes) {
		return nil, errors.New("injected delta failure")
	}
	return driver.RowsAffected(1), nil
}

type questDeltaRows struct {
	cols []string
	rows [][]driver.Value
}

func (r *questDeltaRows) Columns() []string { return r.cols }
func (*questDeltaRows) Close() error        { return nil }
func (r *questDeltaRows) Next(dst []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dst, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
func (c *questDeltaConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	s := c.state.old
	if strings.HasPrefix(q, "SELECT character_id FROM") {
		return &questDeltaRows{[]string{"character_id"}, [][]driver.Value{{"c"}}}, nil
	}
	if strings.Contains(q, "extra_key") {
		rows := [][]driver.Value{}
		for _, k := range sortedStringKeys(s.Extra) {
			rows = append(rows, []driver.Value{int64(1), k, s.Extra[k]})
		}
		return &questDeltaRows{[]string{"quest_id", "extra_key", "extra_value"}, rows}, nil
	}
	return &questDeltaRows{[]string{"quest_id", "status", "trigger_type", "progress_value", "reward_select_index", "multiplier", "state_updated_at"}, [][]driver.Value{{int64(1), s.Status, int64(s.TriggerType), s.ProgressValue, s.RewardSelectIndex, s.Multiplier, s.UpdatedAt}}}, nil
}

func TestQuestDeltaBoundsWritesAndRejectsStalePlan(t *testing.T) {
	for _, mode := range []string{"success", "stale", "failure"} {
		t.Run(mode, func(t *testing.T) {
			old := repository.QuestState{Status: "active", ProgressValue: 2, UpdatedAt: time.Unix(1800000000, 0), Extra: map[string]string{"keep": "yes", "counter": "2"}}
			state := &questDeltaTestDB{old: old}
			if mode == "stale" {
				state.old.ProgressValue = 0
			}
			if mode == "failure" {
				state.failAt = 2
			}
			db := sql.OpenDB(questDeltaConnector{state})
			defer db.Close()
			store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{db}, readDBs: []string{"replica"}, writeDBs: []string{"primary"}, tablePrefix: "dnf_"}}}
			before := repository.QuestRecord{CharacterID: "c", States: map[int64]repository.QuestState{1: old}}
			for i := int64(2); i < 675; i++ {
				before.States[i] = repository.QuestState{Status: "completed", Extra: map[string]string{"reward_state": "granted"}}
			}
			after := repository.CloneQuest(before)
			s := after.States[1]
			s.ProgressValue = 1
			s.Extra["counter"] = "1"
			after.States[1] = s
			err := store.SaveQuestChanges(context.Background(), before, after, repository.QuestFieldStates)
			if mode == "success" {
				if err != nil || state.commit != 1 || state.rollback != 0 || len(state.writes) != 3 {
					t.Fatalf("err=%v state=%+v", err, state)
				}
			} else {
				if err == nil || state.commit != 0 || state.rollback != 1 {
					t.Fatalf("err=%v state=%+v", err, state)
				}
				if mode == "stale" && (!errors.Is(err, repository.ErrQuestVersionConflict) || len(state.writes) != 0) {
					t.Fatal("stale plan wrote assets")
				}
			}
			for _, q := range state.queries {
				if !strings.Contains(q, "FOR UPDATE") || !strings.Contains(q, "`primary`") || strings.Contains(q, "replica") {
					t.Fatal(q)
				}
			}
			for _, w := range state.writes {
				if strings.Contains(w.Query, "DELETE") || strings.Contains(fmt.Sprint(w.Args), "granted") {
					t.Fatalf("history rewritten %+v", w)
				}
			}
		})
	}
}

func TestQuestPlanningRetainsHistoryHeadersButOnlyLoadsActiveExtras(t *testing.T) {
	when := time.Unix(1800000000, 0)
	row := func(id int64, status string) fakeSQLRow {
		return fakeSQLRow{values: []any{id, status, byte(0), int64(2), int64(0), int64(0), when}}
	}
	db := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"c", when}}}, rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{row(1, " IN_PROGRESS "), row(2, "completed")}},
		{rows: []fakeSQLRow{{values: []any{int64(1), "receipt", "keep"}}}},
		{rows: []fakeSQLRow{row(3, "active")}},
		{rows: []fakeSQLRow{{values: []any{int64(3), "receipt", "progress-keep"}}}},
	}}
	g := newTestMySQLGroup(t, db)
	r, ok, err := repository.LoadQuestPlanning(context.Background(), g.Quest, "c")
	if err != nil || !ok || !r.PlanningPartial || r.States[2].Status != "completed" || r.States[1].Extra["receipt"] != "keep" || r.States[2].Extra != nil || r.Progress[3].Extra["receipt"] != "progress-keep" {
		t.Fatalf("record=%+v err=%v", r, err)
	}
	if len(db.execs) != 5 {
		t.Fatalf("expected 5 queries with active quests in both groups, got %d", len(db.execs))
	}
	for _, call := range db.execs {
		if strings.Contains(call.Query, "extra_key") && !strings.Contains(call.Query, "quest_id IN (?)") {
			t.Fatal("unbounded historical extras read")
		}
	}
	if err := g.Quest.Save(context.Background(), repository.CloneQuest(r)); !errors.Is(err, repository.ErrQuestPartialSave) {
		t.Fatalf("partial clone permitted full save: %v", err)
	}
}

func TestQuestDeltaReadbackUsesPrimary(t *testing.T) {
	when := time.Unix(1800000000, 0)
	db := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"c", when}}}, rowsets: []fakeSQLRows{{}, {}, {}, {}}}
	store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: db, readDBs: []string{"replica"}, writeDBs: []string{"primary"}, tablePrefix: "dnf_"}}}
	r, ok, err := store.LoadQuestIDs(context.Background(), "c", []int64{1})
	if err != nil || !ok || !r.PlanningPartial {
		t.Fatalf("readback: %+v %v %v", r, ok, err)
	}
	if len(db.execs) != 5 {
		t.Fatalf("queries: %+v", db.execs)
	}
	for _, call := range db.execs {
		if !strings.Contains(call.Query, "`primary`") || strings.Contains(call.Query, "replica") || strings.Contains(call.Query, "FOR UPDATE") {
			t.Fatalf("readback must use primary without external lock: %s", call.Query)
		}
	}
}
