package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLQuestBatchPreservesLegacySingleRowStream(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "1")
	db := &fakeSQLDB{}
	when := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	states := map[int64]repository.QuestState{
		7: {Status: "complete", UpdatedAt: when},
		3: {Status: "active", TriggerType: 4, ProgressValue: 19, RewardSelectIndex: 2, Multiplier: 3, UpdatedAt: when, Extra: map[string]string{"z": "last", "a": "first"}},
	}
	if err := replaceQuestStates(context.Background(), db, "states", "extras", "77", "states", states); err != nil {
		t.Fatal(err)
	}
	want := []fakeSQLCall{
		{Query: "DELETE FROM extras WHERE character_id = ? AND state_group = ?", Args: []any{"77", "states"}},
		{Query: "DELETE FROM states WHERE character_id = ? AND state_group = ?", Args: []any{"77", "states"}},
		{Query: "INSERT INTO states (character_id, state_group, quest_id, status, trigger_type, progress_value, reward_select_index, multiplier, state_updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", Args: []any{"77", "states", int64(3), states[3].Status, states[3].TriggerType, states[3].ProgressValue, states[3].RewardSelectIndex, states[3].Multiplier, when}},
		{Query: "INSERT INTO extras (character_id, state_group, quest_id, extra_key, extra_value) VALUES (?, ?, ?, ?, ?)", Args: []any{"77", "states", int64(3), "a", "first"}},
		{Query: "INSERT INTO extras (character_id, state_group, quest_id, extra_key, extra_value) VALUES (?, ?, ?, ?, ?)", Args: []any{"77", "states", int64(3), "z", "last"}},
		{Query: "INSERT INTO states (character_id, state_group, quest_id, status, trigger_type, progress_value, reward_select_index, multiplier, state_updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", Args: []any{"77", "states", int64(7), states[7].Status, states[7].TriggerType, states[7].ProgressValue, states[7].RewardSelectIndex, states[7].Multiplier, when}},
	}
	if !reflect.DeepEqual(db.execs, want) {
		t.Fatalf("legacy SQL stream changed:\ngot %#v\nwant %#v", db.execs, want)
	}
}

func TestMySQLQuestBatchPreservesAllRowsAndBoundsRoundTrips(t *testing.T) {
	states := map[int64]repository.QuestState{}
	for i := int64(1); i <= 501; i++ {
		states[i] = repository.QuestState{Status: "complete", TriggerType: byte(i % 17), ProgressValue: i * 9, RewardSelectIndex: i % 4, Multiplier: i % 3, UpdatedAt: time.Unix(i, 0), Extra: map[string]string{"a": fmt.Sprint(i), "z": fmt.Sprint(-i)}}
	}
	for _, group := range []string{"states", "progress"} {
		t.Run(group, func(t *testing.T) {
			rows := func(batch string) (map[string][]any, int) {
				t.Setenv(currentMysqlInsertBatchEnv, batch)
				db := &fakeSQLDB{}
				if err := replaceQuestStates(context.Background(), db, "states", "extras", "77", group, states); err != nil {
					t.Fatal(err)
				}
				out := map[string][]any{}
				for _, call := range db.execs[2:] {
					table, width := "states", 9
					if strings.HasPrefix(call.Query, "INSERT INTO extras ") {
						table, width = "extras", 5
					}
					if len(call.Args)%width != 0 || len(call.Args)/width > 200 {
						t.Fatalf("unbounded/misaligned SQL: %s", call.Query)
					}
					if strings.Count(call.Query, "?") != len(call.Args) {
						t.Fatal("placeholder/argument count mismatch")
					}
					out[table] = append(out[table], call.Args...)
				}
				return out, len(db.execs)
			}
			legacy, oldCount := rows("1")
			batched, newCount := rows("200")
			if !reflect.DeepEqual(legacy, batched) {
				t.Fatal("batched quest rows, extras, ordering or timestamps differ")
			}
			if oldCount != 1505 || newCount != 11 {
				t.Fatalf("round trips = %d -> %d, want 1505 -> 11", oldCount, newCount)
			}
		})
	}
}

func TestMySQLQuestBatchEmptyGroupStillClearsOnlySelectedGroup(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	db := &fakeSQLDB{}
	if err := replaceQuestStates(context.Background(), db, "states", "extras", "77", "progress", nil); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 2 {
		t.Fatalf("empty group wrote %d statements", len(db.execs))
	}
	for _, call := range db.execs {
		if !reflect.DeepEqual(call.Args, []any{"77", "progress"}) {
			t.Fatal("delete escaped owner/group")
		}
	}
}

type questBatchConnector struct {
	state  *inventoryGroupSQLState
	failAt int
}

func (c questBatchConnector) Connect(context.Context) (driver.Conn, error) {
	return questBatchConn{inventoryGroupConn{c.state}, c.failAt}, nil
}
func (c questBatchConnector) Driver() driver.Driver { return inventoryGroupDriver{c.state} }

type questBatchConn struct {
	inventoryGroupConn
	failAt int
}

func (c questBatchConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.writes++
	if c.state.writes == c.failAt {
		return nil, errors.New("injected quest batch write failure")
	}
	return driver.RowsAffected(1), nil
}

func TestMySQLQuestBatchResidualFailureRollsBackBothTables(t *testing.T) {
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	// Parent upsert, two deletes, state flush, extra flush are all one SQL tx.
	for _, failAt := range []int{4, 5, 0} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			state := &inventoryGroupSQLState{}
			db := sql.OpenDB(questBatchConnector{state, failAt})
			defer db.Close()
			store := &mysqlQuestStore{mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{DB: db}, writeDBs: []string{"dnf_s1_w1"}, tablePrefix: "dnf_"}}}
			record := repository.QuestRecord{CharacterID: "77", States: map[int64]repository.QuestState{3: {Status: "active", Extra: map[string]string{"rule": "pvf"}}}}
			err := store.SaveFields(context.Background(), record, repository.QuestFieldStates)
			if failAt == 0 {
				if err != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatalf("success transaction: %v %+v", err, state)
				}
			} else if err == nil || state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("partial write not rolled back: %v %+v", err, state)
			}
			if state.begins != 1 {
				t.Fatalf("begins=%d", state.begins)
			}
		})
	}
}
