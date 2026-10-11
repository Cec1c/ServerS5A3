package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

// Explicit SQL driver fault model: real production UOW/SQL repos/RewardOwner
// execute unchanged. It models both possible Commit-error outcomes, not a live
// MySQL server outage. Only persisted receipt rows and inserted mail counts
// are retained; no arbitrary SQL success is claimed.
type raidCommitSQLState struct {
	active                             bool
	begins, commits, rollbacks, writes int
	durable, staged                    map[string]int64
	mails, stagedMails                 int
	outcome                            string
	queries                            []string
}
type raidCommitSQLConnector struct{ s *raidCommitSQLState }

func (c raidCommitSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return raidCommitSQLConn{c.s}, nil
}
func (c raidCommitSQLConnector) Driver() driver.Driver { return raidCommitSQLDriver{c.s} }

type raidCommitSQLDriver struct{ s *raidCommitSQLState }

func (c raidCommitSQLDriver) Open(string) (driver.Conn, error) { return raidCommitSQLConn{c.s}, nil }

type raidCommitSQLConn struct{ s *raidCommitSQLState }

func (c raidCommitSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c raidCommitSQLConn) Close() error { return nil }
func (c raidCommitSQLConn) Begin() (driver.Tx, error) {
	if c.s.active {
		return nil, errors.New("overlapping driver transaction")
	}
	c.s.active = true
	c.s.begins++
	c.s.staged = map[string]int64{}
	for k, v := range c.s.durable {
		c.s.staged[k] = v
	}
	c.s.stagedMails = c.s.mails
	return raidCommitSQLTx{c.s}, nil
}
func (c raidCommitSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c raidCommitSQLConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if !c.s.active {
		return nil, errors.New("query outside physical transaction")
	}
	c.s.queries = append(c.s.queries, q)
	fields := strings.Split(strings.Split(strings.TrimPrefix(q, "SELECT "), " FROM ")[0], ",")
	rows := &pairSQLRows{columns: fields}
	switch {
	case strings.HasPrefix(q, "SELECT character_id, account_id,"):
		vals := make([]driver.Value, len(fields))
		for n := range vals {
			vals[n] = int64(0)
		}
		vals[0], vals[1], vals[2], vals[3], vals[4], vals[5] = "19", "a", int64(0), "name", int64(0), int64(90)
		vals[len(vals)-1], vals[len(vals)-2] = nil, nil
		rows.rows = [][]driver.Value{vals}
	case strings.HasPrefix(q, "SELECT stat_key, stat_value FROM "):
		for k, v := range c.s.staged {
			rows.rows = append(rows.rows, []driver.Value{k, v})
		}
	case strings.HasPrefix(q, "SELECT entry_key, item_id, item_count") && strings.Contains(q, "_inventory_items"):
		rows.rows = [][]driver.Value{{"0:50", int64(10096296), int64(1), false, nil, nil}}
	case strings.HasPrefix(q, "SELECT character_id, updated_at"):
		rows.rows = [][]driver.Value{{"19", nil}}
	}
	return rows, nil
}
func (c raidCommitSQLConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !c.s.active {
		return nil, errors.New("write outside physical transaction")
	}
	c.s.writes++
	if c.s.outcome == "late_mail_failure" && strings.HasPrefix(q, "INSERT INTO ") && strings.Contains(q, "_mails`") {
		return nil, errors.New("late physical mail write failure fixture")
	}
	if strings.Contains(q, "_character_stats") {
		if strings.HasPrefix(q, "DELETE FROM ") {
			c.s.staged = map[string]int64{}
		}
		if strings.HasPrefix(q, "INSERT INTO ") {
			if len(args) != 3 {
				return nil, errors.New("unexpected stat insert shape")
			}
			key, ok := args[1].Value.(string)
			v, vok := args[2].Value.(int64)
			if !ok || !vok {
				return nil, errors.New("unexpected stat value")
			}
			c.s.staged[key] = v
		}
	}
	if strings.HasPrefix(q, "DELETE FROM ") && strings.Contains(q, "_mails`") {
		c.s.stagedMails = 0
	}
	if strings.HasPrefix(q, "INSERT INTO ") && strings.Contains(q, "_mails`") {
		c.s.stagedMails++
	}
	return driver.RowsAffected(1), nil
}

type raidCommitSQLTx struct{ s *raidCommitSQLState }

func (tx raidCommitSQLTx) Commit() error {
	if !tx.s.active {
		return errors.New("inactive commit")
	}
	tx.s.commits++
	tx.s.active = false
	if tx.s.commits == 1 && tx.s.outcome == "not_durable" {
		return errors.New("SQL commit response lost, transaction not durable fixture")
	}
	tx.s.durable = tx.s.staged
	tx.s.mails = tx.s.stagedMails
	if tx.s.commits == 1 {
		return errors.New("SQL commit response lost after durable transaction fixture")
	}
	return nil
}
func (tx raidCommitSQLTx) Rollback() error {
	if tx.s.active {
		tx.s.rollbacks++
		tx.s.active = false
	}
	return nil
}
func TestMySQLRaidRewardUnknownCommitReceiptReplay(t *testing.T) {
	for _, outcome := range []string{"durable", "not_durable"} {
		t.Run(outcome, func(t *testing.T) {
			state := &raidCommitSQLState{outcome: outcome, durable: map[string]int64{}}
			db := sql.OpenDB(raidCommitSQLConnector{state})
			defer db.Close()
			u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			owner, e := raid.NewRewardOwner(repo.Group{AccountAssets: u})
			if e != nil {
				for _, q := range state.queries {
					t.Log(q)
				}
				t.Fatalf("err=%v begin=%d commit=%d", e, state.begins, state.commits)
			}
			projects := 0
			cmd := raid.RewardCommand{AccountID: "a", CharacterID: "19", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 0, Kind: 3, Card: 0, MinimumClears: 1, OwnedClears: 1, Fingerprint: [32]byte{1}, CompletedAt: time.Now().UTC(), Project: func(a *raid.RewardAssets) (raid.RewardChanges, error) {
				projects++
				_, e := repo.AppendSystemMail(context.Background(), a.Mailbox, repo.SystemMailDelivery{RecipientCharacterID: "19", Source: "raid_reward_fault_model", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}})
				return raid.RewardChanges{Mailbox: true}, e
			}}
			if _, e := owner.Grant(context.Background(), cmd); e == nil {
				t.Fatal("commit error not exercised")
			}
			firstWrites := state.writes
			r, e := owner.Grant(context.Background(), cmd)
			if e != nil {
				for _, q := range state.queries {
					t.Log(q)
				}
				t.Fatalf("err=%v begin=%d commit=%d", e, state.begins, state.commits)
			}
			if outcome == "durable" {
				if !r.Replayed || projects != 1 || state.writes != firstWrites {
					t.Fatal("durable receipt did not suppress second projection", r, projects, state.writes, firstWrites)
				}
			} else {
				if r.Replayed || projects != 2 || state.writes <= firstWrites {
					t.Fatal("non-durable retry did not grant exactly once", r, projects)
				}
			}
			if state.mails != 1 || len(state.durable) != 6 || state.begins != 2 || state.commits != 2 {
				t.Fatalf("receipt and mail not same durable tx: %+v", state)
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") || ((strings.HasPrefix(q, "SELECT character_id, account_id,") || strings.HasPrefix(q, "SELECT character_id, updated_at")) && !strings.HasSuffix(q, " FOR UPDATE")) {
					t.Fatal("unlocked or readonly query", q)
				}
			}
			cmd.Card = 1
			if _, e := owner.Grant(context.Background(), cmd); !errors.Is(e, raid.ErrRewardReceipt) {
				t.Fatal("unknown-commit changed-card replay", e)
			}
			cmd.Card = 0
			cmd.Fingerprint[1] = 9
			if _, e := owner.Grant(context.Background(), cmd); !errors.Is(e, raid.ErrRewardReceipt) {
				t.Fatal("unknown-commit changed-plan replay", e)
			}
			if projects != (map[string]int{"durable": 1, "not_durable": 2}[outcome]) || state.mails != 1 {
				t.Fatal("conflicting replay projected again")
			}
		})
	}
}

func TestMySQLRaidRewardLateMailWriteRollsBackPriorStatements(t *testing.T) {
	state := &raidCommitSQLState{outcome: "late_mail_failure", durable: map[string]int64{"keep": 7}, mails: 1}
	db := sql.OpenDB(raidCommitSQLConnector{state})
	defer db.Close()
	u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
	ctx := context.Background()
	e := u.WithinAccountCharacterMailboxAssets(ctx, "a", "19", func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, eq repo.EquipmentRepository, m repo.MailboxRepository) error {
		if e := a.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: 2}}}); e != nil {
			return e
		}
		cr, _, e := c.Load(ctx, "19")
		if e != nil {
			return e
		}
		cr.Stats["receipt"] = 1
		if e := repo.SaveCharacterFields(ctx, c, cr, repo.CharacterFieldStats); e != nil {
			return e
		}
		if e := i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 2}}}); e != nil {
			return e
		}
		_, e = repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "19", Source: "raid_reward_sql_fault_model", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}})
		return e
	})
	if e == nil || state.active || state.commits != 0 || state.rollbacks != 1 || state.writes < 4 || len(state.durable) != 1 || state.durable["keep"] != 7 || state.mails != 1 {
		t.Fatalf("late mail write not atomic err=%v state=%+v", e, state)
	}
}
