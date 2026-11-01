package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	mysqldriver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type activitySQLState struct {
	accountMailSQLState
	event      repo.ActivityEvent
	statements []string
}
type activitySQLConnector struct{ s *activitySQLState }

func (c activitySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return activitySQLConn{accountMailSQLConn{&c.s.accountMailSQLState}, c.s}, nil
}
func (c activitySQLConnector) Driver() driver.Driver {
	return accountMailSQLDriver{&c.s.accountMailSQLState}
}

type activitySQLConn struct {
	accountMailSQLConn
	state *activitySQLState
}

func (c activitySQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "SELECT character_id, account_id FROM ") {
		c.state.queries = append(c.state.queries, q)
		return &pairSQLRows{columns: []string{"character_id", "account_id"}, rows: [][]driver.Value{{"19", "a"}}}, nil
	}
	if strings.HasPrefix(q, "SELECT account_id, name FROM ") {
		c.state.queries = append(c.state.queries, q)
		account := "a"
		if c.state.scenario == "foreign_owner" {
			account = "b"
		}
		return &pairSQLRows{columns: []string{"account_id", "name"}, rows: [][]driver.Value{{account, "角色19"}}}, nil
	}
	if strings.HasPrefix(q, "SELECT "+activityColumns) {
		c.state.queries = append(c.state.queries, q)
		e := c.state.event
		if c.state.scenario == "conflict" {
			e.DungeonID++
		}
		return &pairSQLRows{columns: strings.Split(activityColumns, ", "), rows: [][]driver.Value{{e.ID, e.AccountID, e.CharacterID, e.CharacterName, e.Metric, e.Amount, e.OccurredAt, e.DungeonID, int64(e.ItemID), e.Rarity, e.Source}}}, nil
	}
	return c.accountMailSQLConn.QueryContext(ctx, q, args)
}
func (c activitySQLConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.state.statements = append(c.state.statements, q)
	if strings.Contains(q, "activity_events") && (c.state.scenario == "duplicate" || c.state.scenario == "conflict") {
		return nil, &mysqldriver.MySQLError{Number: 1062, Message: "重复事件"}
	}
	if strings.Contains(q, "activity_counters") && c.state.scenario == "counter_failure" {
		return nil, errors.New("模拟汇总写入失败")
	}
	return c.accountMailSQLConn.ExecContext(ctx, q, args)
}
func TestMySQLActivityTransactionRoutingReplayAndRollback(t *testing.T) {
	for _, mode := range []string{"success", "duplicate", "conflict", "foreign_owner", "counter_failure", "write_failure", "late_abort", "commit_failure", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			e := repo.ActivityEvent{ID: "run:19:fatigue:0:0", AccountID: "a", CharacterID: "19", Metric: repo.ActivityFatigue, Amount: 1, OccurredAt: time.Now().UTC(), DungeonID: 310, Source: "dungeon_room"}
			state := &activitySQLState{accountMailSQLState: accountMailSQLState{scenario: mode}, event: e}
			db := sql.OpenDB(activitySQLConnector{state})
			defer db.Close()
			u := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				got = u.WithinCharacterSettlement(ctx, "19", func(tx repo.Group) error {
					added, err := tx.Activity.Record(ctx, e)
					if err != nil {
						return err
					}
					if added == (mode == "duplicate") {
						t.Fatal("重复事件判断错误")
					}
					if mode == "late_abort" {
						return errors.New("后续资产失败")
					}
					if mode == "cancel" {
						cancel()
					}
					if mode == "panic" {
						panic("模拟事务异常")
					}
					return nil
				})
			}()
			if mode == "success" || mode == "duplicate" {
				if got != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatalf("正常事务错误：%v %+v", got, state)
				}
			} else {
				if got == nil && !panicked {
					t.Fatal("失败没有传播")
				}
				if mode != "commit_failure" && (state.commits != 0 || state.rollbacks != 1) {
					t.Fatalf("失败提交：%+v", state)
				}
			}
			if mode == "success" && len(state.statements) != 2 {
				t.Fatal("有效事件写入次数异常", state.statements)
			}
			if mode == "duplicate" && len(state.statements) != 1 {
				t.Fatal("重放更新了汇总")
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") || (!strings.HasSuffix(q, "FOR UPDATE") && !strings.HasPrefix(q, "SELECT character_id, account_id FROM ")) {
					t.Fatal("读取脱离写事务", q)
				}
			}
		})
	}
}

func TestMySQLActivityOriginalAssetTransactions(t *testing.T) {
	for _, kind := range []string{"epic_entry", "arcade"} {
		for _, mode := range []string{"success", "duplicate", "foreign_owner", "counter_failure", "late_abort", "commit_failure", "panic"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				e := repo.ActivityEvent{ID: "asset:19:payment", AccountID: "a", CharacterID: "19", Metric: repo.ActivityFatigue, Amount: 1, OccurredAt: time.Now(), DungeonID: 6063, Source: kind}
				if kind == "arcade" {
					e.Amount = 40
					e.DungeonID = 0
				}
				state := &activitySQLState{accountMailSQLState: accountMailSQLState{scenario: mode}, event: e}
				db := sql.OpenDB(activitySQLConnector{state})
				defer db.Close()
				router := mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}
				apply := func(chars repo.CharacterRepository) error {
					added, err := repo.RecordCharacterActivity(context.Background(), chars, e)
					if err != nil {
						return err
					}
					if added == (mode == "duplicate") {
						t.Fatal("重放判断错误")
					}
					if mode == "late_abort" {
						return errors.New("后续资产失败")
					}
					if mode == "panic" {
						panic("模拟原事务异常")
					}
					return nil
				}
				var err error
				panicked := false
				func() {
					defer func() { panicked = recover() != nil }()
					if kind == "epic_entry" {
						err = (&mysqlCharacterAssetUnitOfWork{router: router}).WithinCharacterAssets(context.Background(), "19", func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
							return apply(c)
						})
					} else {
						err = (&mysqlRentalAssetUnitOfWork{router: router}).WithinRentalAssets(context.Background(), "a", "19", func(_ repo.AccountRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
							return apply(c)
						})
					}
				}()
				if mode == "success" || mode == "duplicate" {
					if err != nil || panicked || state.commits != 1 || state.rollbacks != 0 {
						t.Fatal(err, panicked, state)
					}
				} else {
					if err == nil && !panicked {
						t.Fatal("失败未传播")
					}
					if mode != "commit_failure" && (state.commits != 0 || state.rollbacks != 1) {
						t.Fatal(state)
					}
				}
				characterTable, _ := router.writeTable(mysqlCharTable, "19")
				if len(state.queries) == 0 || !strings.Contains(state.queries[0], characterTable) {
					t.Fatal("所有权分库错误", state.queries)
				}
				eventTable, _ := router.writeTable(activityEventsTable, "a")
				for _, query := range state.queries {
					if strings.Contains(query, "readonly") || !strings.HasSuffix(query, "FOR UPDATE") {
						t.Fatal("读取脱离原事务", query)
					}
				}
				if len(state.statements) > 0 && !strings.Contains(state.statements[0], eventTable) {
					t.Fatal("账本分库错误", state.statements)
				}
			})
		}
	}
}
