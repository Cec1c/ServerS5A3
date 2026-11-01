package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type epicSourceSQLState struct {
	query    string
	args     []driver.NamedValue
	calls    int
	negative bool
}
type epicSourceSQLConnector struct{ state *epicSourceSQLState }

func (c epicSourceSQLConnector) Driver() driver.Driver {
	return accountMailSQLDriver{&accountMailSQLState{}}
}
func (c epicSourceSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return epicSourceSQLConn{accountMailSQLConn{&accountMailSQLState{}}, c.state}, nil
}

type epicSourceSQLConn struct {
	accountMailSQLConn
	state *epicSourceSQLState
}

func (c epicSourceSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.query = q
	c.state.args = append([]driver.NamedValue{}, args...)
	c.state.calls++
	amount := int64(2)
	if c.state.negative {
		amount = -1
	}
	return &pairSQLRows{columns: []string{"character_id", "metric", "amount", "dungeon_id", "epic_source"}, rows: [][]driver.Value{
		{"1", repo.ActivityEpic, int64(5), int64(0), int64(0)},
		{"1", repo.ActivityFatigue, int64(20), int64(0), int64(0)},
		{"1", repo.ActivityEpic, amount, int64(316), int64(1)},
		{"1", repo.ActivityEpic, int64(3), int64(6065), int64(1)},
	}}, nil
}

func TestMySQLActivityEpicSourcesSingleReadViewAndBoundScopes(t *testing.T) {
	for _, daily := range []bool{false, true} {
		t.Run(map[bool]string{false: "累计", true: "角色服务日"}[daily], func(t *testing.T) {
			state := &epicSourceSQLState{}
			db := sql.OpenDB(epicSourceSQLConnector{state})
			defer db.Close()
			store := &mysqlActivityStore{mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"read_a", "read_b"}, writeDBs: []string{"write_a", "write_b"}, tablePrefix: "dnf_", now: time.Now}}}
			q := repo.ActivityQuery{AccountID: "a", IncludeEpicSources: true}
			want := []any{"a", "a", repo.ActivityEpic}
			if daily {
				q.CharacterID = "1"
				q.FromDay = "2026-10-26"
				q.ThroughDay = "2026-11-01"
				want = []any{"a", "1", q.FromDay, q.ThroughDay, "a", "1", repo.ActivityEpic, q.FromDay, q.ThroughDay}
			}
			got, err := store.Summary(context.Background(), q)
			if err != nil || got.Metrics[repo.ActivityEpic] != 5 || len(got.EpicSources) != 2 {
				t.Fatalf("来源聚合错误：%+v %v", got, err)
			}
			if state.calls != 1 || !strings.Contains(state.query, " UNION ALL ") || strings.Contains(state.query, "write_") || strings.Contains(state.query, "FOR UPDATE") || strings.Contains(state.query, "LIMIT") {
				t.Fatal("分项脱离同一只读视图", state.query)
			}
			values := []any{}
			for _, arg := range state.args {
				values = append(values, arg.Value)
			}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("参数归属/服务日错误：%v/%v", values, want)
			}
			if daily && (!strings.Contains(state.query, "bucket >= ?") || !strings.Contains(state.query, "service_day >= ?")) {
				t.Fatal("总量和分项周期不同", state.query)
			}
			if got.EpicSources[0].DungeonID != 316 || got.EpicSources[0].Amount != 2 {
				t.Fatal(got.EpicSources)
			}
		})
	}
}
