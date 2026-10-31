package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Gold is a typed character column; receipts are dynamic stats. Extend the
// receipt driver's model to keep both in the same physical commit/rollback.
type imageCommunicationSQLState struct {
	*raidCommitSQLState
	gold, stagedGold int64
}
type imageCommunicationSQLConnector struct{ state *imageCommunicationSQLState }

func (c imageCommunicationSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return imageCommunicationSQLConn{raidCommitSQLConn{c.state.raidCommitSQLState}, c.state}, nil
}
func (c imageCommunicationSQLConnector) Driver() driver.Driver {
	return imageCommunicationSQLDriver{c.state}
}

type imageCommunicationSQLDriver struct{ state *imageCommunicationSQLState }

func (d imageCommunicationSQLDriver) Open(string) (driver.Conn, error) {
	return imageCommunicationSQLConnector{d.state}.Connect(context.Background())
}

type imageCommunicationSQLConn struct {
	raidCommitSQLConn
	state *imageCommunicationSQLState
}

func (c imageCommunicationSQLConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	tx, err := c.raidCommitSQLConn.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	c.state.stagedGold = c.state.gold
	return imageCommunicationSQLTx{tx, c.state}, nil
}
func (c imageCommunicationSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.raidCommitSQLConn.QueryContext(ctx, q, args)
	if err == nil && strings.HasPrefix(q, "SELECT character_id, account_id,") {
		r := rows.(*pairSQLRows)
		for i, col := range r.columns {
			if strings.Trim(col, " `") == "gold" {
				r.rows[0][i] = c.state.stagedGold
			}
		}
	}
	return rows, err
}
func (c imageCommunicationSQLConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "INSERT INTO ") && strings.Contains(q, "_characters` (") {
		columns := strings.SplitN(strings.SplitN(q, "(", 2)[1], ")", 2)[0]
		for i, col := range strings.Split(columns, ",") {
			if strings.Trim(col, " `") == "gold" {
				c.state.stagedGold = args[i].Value.(int64)
			}
		}
	}
	return c.raidCommitSQLConn.ExecContext(ctx, q, args)
}

type imageCommunicationSQLTx struct {
	driver.Tx
	state *imageCommunicationSQLState
}

func (t imageCommunicationSQLTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil {
		t.state.gold = t.state.stagedGold
	}
	return err
}

// Exercise the actual SQL transaction router with the existing deterministic
// driver. This verifies writer routing, locking and rollback, not a live DB.
func TestImageCommunicationAssetQuestSQLTransaction(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "foreign-quest"} {
		t.Run(mode, func(t *testing.T) {
			state := &imageCommunicationSQLState{raidCommitSQLState: &raidCommitSQLState{commits: 1, durable: map[string]int64{}}, gold: 20000}
			db := sql.OpenDB(imageCommunicationSQLConnector{state})
			defer db.Close()
			u := &mysqlCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"stale_replica"}, writeDBs: []string{"writer"}, tablePrefix: "dnf_", now: time.Now}}
			err := u.WithinCharacterAssetsAndQuest(context.Background(), "19", func(chars repo.CharacterRepository, quests repo.QuestReader) error {
				c, found, err := chars.Load(context.Background(), "19")
				if err != nil || !found {
					t.Fatalf("character found=%v err=%v", found, err)
				}
				q, found, err := quests.Load(context.Background(), "19")
				if err != nil || !found || q.CharacterID != "19" {
					t.Fatalf("quest=%+v found=%v err=%v", q, found, err)
				}
				c.Stats["gold"] = 10000
				c.Stats["image_communication_receipt_version"] = 1
				if err := repo.SaveCharacterFields(context.Background(), chars, c, repo.CharacterFieldStats); err != nil {
					return err
				}
				if mode == "rollback" {
					return errors.New("after wallet and receipt writes")
				}
				if mode == "foreign-quest" {
					_, _, err = quests.Load(context.Background(), "20")
					return err
				}
				return nil
			})
			if mode == "commit" {
				if err != nil || state.gold != 10000 || state.durable["image_communication_receipt_version"] != 1 || state.commits != 2 {
					t.Fatalf("commit err=%v gold=%d receipt=%d commits=%d", err, state.gold, state.durable["image_communication_receipt_version"], state.commits)
				}
			} else if err == nil || state.gold != 20000 || state.durable["image_communication_receipt_version"] != 0 || state.rollbacks != 1 {
				t.Fatalf("rollback err=%v state=%+v", err, state)
			}
			if state.begins != 1 {
				t.Fatalf("transactions=%d", state.begins)
			}
			accountLock := -1
			for index, query := range state.queries {
				if strings.Contains(query, "`dnf__"+mysqlAccountTable+"`") && strings.HasSuffix(query, "FOR UPDATE") {
					accountLock = index
					break
				}
			}
			if accountLock < 0 {
				t.Fatalf("account FOR UPDATE was not acquired: %v", state.queries)
			}
			for index, query := range state.queries {
				if strings.Contains(query, "stale_replica") {
					t.Fatalf("replica read escaped the writer transaction: %s", query)
				}
				if index < accountLock {
					if strings.HasSuffix(query, "FOR UPDATE") {
						t.Fatalf("asset read locked before ownership/account lock: %s", query)
					}
					continue
				}
				if !strings.HasSuffix(query, "FOR UPDATE") {
					t.Fatalf("asset read after account lock is unlocked: %s", query)
				}
			}
		})
	}
}
