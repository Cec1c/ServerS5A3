package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type rankSQLState struct {
	accountMailSQLState
	old        []byte
	queries    []string
	writes     []string
	parameters [][]driver.NamedValue
	payload    []byte
	rewards    int
	failReward bool
}
type rankSQLConnector struct{ s *rankSQLState }

func (c rankSQLConnector) Driver() driver.Driver {
	return accountMailSQLDriver{&c.s.accountMailSQLState}
}
func (c rankSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return rankSQLConn{accountMailSQLConn{&c.s.accountMailSQLState}, c.s}, nil
}

type rankSQLConn struct {
	accountMailSQLConn
	s *rankSQLState
}

func (c rankSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.queries = append(c.s.queries, q)
	c.s.parameters = append(c.s.parameters, append([]driver.NamedValue(nil), args...))
	if strings.HasPrefix(q, "SELECT snapshot_json") {
		return &pairSQLRows{columns: []string{"snapshot_json"}, rows: [][]driver.Value{{c.s.old}}}, nil
	}
	if strings.HasPrefix(q, "SELECT account_id, character_id, SUM(total)") {
		return &pairSQLRows{columns: []string{"account_id", "character_id", "total"}, rows: [][]driver.Value{{"a", "1", int64(20)}, {"a", "2", int64(10)}, {"b", "3", int64(30)}}}, nil
	}
	return nil, errors.New("未预期的排行榜SQL：" + q)
}
func (c rankSQLConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.s.writes = append(c.s.writes, q)
	if strings.Contains(q, "activity_rank_rewards") {
		if c.s.failReward {
			return nil, errors.New("奖励资格写入故障")
		}
		c.s.rewards++
	}
	if strings.HasPrefix(q, "UPDATE ") {
		c.s.payload = append([]byte(nil), args[0].Value.([]byte)...)
	}
	return driver.RowsAffected(1), nil
}
func rankSQLStore(t *testing.T, state *rankSQLState) *mysqlActivityStore {
	t.Helper()
	db := sql.OpenDB(rankSQLConnector{state})
	t.Cleanup(func() { db.Close() })
	return &mysqlActivityStore{mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"replica"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}}
}
func TestMySQLActivityRankPrimaryUnionWindowAndMetric(t *testing.T) {
	for _, period := range []string{"day", "week", "all"} {
		t.Run(period, func(t *testing.T) {
			s := &rankSQLState{}
			store := rankSQLStore(t, s)
			w, _ := repo.ActivityRankPeriod(period, time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
			rows, err := store.RankContributions(context.Background(), repo.ActivityEpic, w)
			if err != nil || len(rows) != 3 || len(s.queries) != 1 {
				t.Fatal(rows, err, s.queries)
			}
			q := s.queries[0]
			if strings.Contains(q, "replica") || !strings.Contains(q, " UNION ALL ") || !strings.Contains(q, "shard_a") || !strings.Contains(q, "shard_b") || strings.Contains(q, "activity_events") {
				t.Fatal("排行榜未复用主库汇总", q)
			}
			for _, v := range s.parameters[0] {
				if v.Ordinal == 1 && v.Value != repo.ActivityEpic {
					t.Fatal("指标未绑定", v)
				}
			}
			if period == "all" && !strings.Contains(q, "bucket = 'all'") || period != "all" && !strings.Contains(q, "bucket >= ? AND bucket <= ?") {
				t.Fatal(q)
			}
		})
	}
}

func TestMySQLActivityRankConsumptionBoundSources(t *testing.T) {
	for _, metric := range []string{repo.ActivityColorless, repo.ActivityGoldSpent} {
		for _, period := range []string{"day", "week", "all"} {
			state := &rankSQLState{}
			store := rankSQLStore(t, state)
			window, _ := repo.ActivityRankPeriod(period, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
			_, err := store.RankContributions(context.Background(), metric, window)
			if err != nil {
				t.Fatal(err)
			}
			query := state.queries[0]
			sources := repo.ActivityRankSourceMetrics(metric)
			if metric == repo.ActivityGoldSpent && !strings.Contains(query, "metric IN (?,?,?,?,?)") {
				t.Fatal(query)
			}
			pos := 0
			for range store.router.writeDBs {
				for _, source := range sources {
					if state.parameters[0][pos].Value != source {
						t.Fatal(state.parameters)
					}
					pos++
				}
				if period != "all" {
					pos += 2
				}
			}
			if pos != len(state.parameters[0]) || strings.Contains(query, "activity_events") {
				t.Fatal(query, state.parameters)
			}
		}
	}
}
func TestMySQLActivityRankWeeklyAtomicReplayConflictAndFailure(t *testing.T) {
	req := repo.ActivityRankSettlement{Metric: repo.ActivityFatigue, Week: "2026-10-26", Now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), Policy: repo.ActivityRankPolicy{Version: "v1", Rewards: []repo.ActivityRankRewardTier{{FromRank: 1, ThroughRank: 1, AppearanceCode: "appearance:crown"}}}}
	for _, mode := range []string{"success", "reward_failure", "commit_failure", "replay", "policy_conflict", "corrupt_snapshot"} {
		t.Run(mode, func(t *testing.T) {
			state := &rankSQLState{}
			if mode == "reward_failure" {
				state.failReward = true
			}
			if mode == "commit_failure" {
				state.scenario = "commit_failure"
			}
			if mode == "replay" || mode == "policy_conflict" {
				old, err := repo.NewActivityRankSnapshot(req, []repo.ActivityRankContribution{{AccountID: "a", CharacterID: "1", Score: 30}})
				if err != nil {
					t.Fatal(err)
				}
				state.old, _ = json.Marshal(old)
			}
			if mode == "corrupt_snapshot" {
				state.old = []byte("broken")
			}
			local := req
			if mode == "policy_conflict" {
				local.Policy.Version = "v2"
			}
			store := rankSQLStore(t, state)
			v, created, err := store.SettleRankWeek(context.Background(), local)
			if mode == "success" {
				if err != nil || !created || len(v.Rewards) != 2 || state.rewards != 2 || state.commits != 1 || len(state.payload) == 0 {
					t.Fatal(v, created, err, state)
				}
				if !strings.Contains(state.queries[0], "FOR UPDATE") {
					t.Fatal("结算未先锁唯一周记录")
				}
			} else if mode == "replay" {
				if err != nil || created || state.rewards != 0 || len(state.queries) != 1 || state.commits != 1 {
					t.Fatal(v, created, err, state)
				}
			} else {
				if err == nil || created {
					t.Fatal("失败结算发布成功", mode, created, err)
				}
				if mode != "commit_failure" && state.rollbacks != 1 {
					t.Fatal("失败未回滚", state.rollbacks)
				}
			}
		})
	}
}
