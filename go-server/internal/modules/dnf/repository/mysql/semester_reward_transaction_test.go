package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type semesterRewardSQLConnector struct {
	s *semesterSQLState
	k string
}

func (c semesterRewardSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return semesterRewardSQLConn{semesterSQLConn{c.s}, c.k}, nil
}
func (c semesterRewardSQLConnector) Driver() driver.Driver { return semesterSQLDriver{c.s} }

type semesterRewardSQLConn struct {
	semesterSQLConn
	k string
}

func (c semesterRewardSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, e := c.semesterSQLConn.QueryContext(ctx, q, args)
	if e != nil {
		return nil, e
	}
	if strings.HasPrefix(q, "SELECT stat_key, stat_value ") {
		return &pairSQLRows{columns: []string{"stat_key", "stat_value"}, rows: [][]driver.Value{{c.k + ":day_bitmap", int64((1 << 24) - 1)}, {c.k + ":qualified_days", int64(24)}, {c.k + ":version", int64(1)}, {"other", int64(888)}}}, nil
	}
	return rows, nil
}
func TestSemesterRewardActualSQLTransaction(t *testing.T) {
	for _, mode := range []string{"success", "write-failure", "cancel", "panic", "commit-unknown"} {
		t.Run(mode, func(t *testing.T) {
			start := time.Date(2026, 10, 10, 6, 0, 0, 0, oe.ChinaCalendar)
			campaign := oe.EpicCampaign{Version: 1, Starts: start, Ends: start.AddDate(0, 0, 28), CreatedAt: start}
			now := campaign.Ends.Add(-time.Hour)
			key := oe.SemesterJournalKey(campaign)
			state := newSemesterSQLState(mode)
			db := sql.OpenDB(semesterRewardSQLConnector{state, key})
			defer db.Close()
			u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := oe.SemesterClaimCommand{AccountID: "a1", CharacterID: "1", Campaign: campaign, Now: now, Validate: func() bool { return true }}
			cmd.Deliver = func(m repo.MailboxRepository, r oe.AttendancePVFItemReward, k string) (string, error) {
				id, e := repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: k, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
				if e != nil {
					return id, e
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "panic" {
					panic("after physical mail writes")
				}
				return id, nil
			}
			cat := &oe.SemesterCatalog{Rewards: [3]oe.AttendancePVFItemReward{{ItemID: 490010219, Count: 1}, {ItemID: 490010217, Count: 1}, {ItemID: 490010215, Count: 1}}}
			_, e := oe.ClaimSemesterReward(ctx, repo.Group{MailboxAssets: u}, cat, cmd)
			if mode != "success" && mode != "commit-unknown" {
				// Error paths release the physical transaction. On a canceled context
				// database/sql performs that release from its async awaitDone goroutine,
				// so join on the driver rollback before sampling the counters instead of
				// racing it.
				state.awaitRollback(t)
			}
			begins, commits, rollbacks, writes := state.snapshot()
			if begins != 1 || writes == 0 {
				t.Fatalf("transaction not exercised: state=%s err=%v", state.describe(), e)
			}
			if mode == "success" {
				if e != nil || commits != 1 || rollbacks != 0 {
					t.Fatalf("state=%s err=%v", state.describe(), e)
				}
			} else if mode == "commit-unknown" {
				if e == nil || commits != 1 {
					t.Fatalf("state=%s err=%v", state.describe(), e)
				}
			} else if e == nil || commits != 0 || rollbacks != 1 {
				t.Fatalf("state=%s err=%v", state.describe(), e)
			}
		})
	}
}
