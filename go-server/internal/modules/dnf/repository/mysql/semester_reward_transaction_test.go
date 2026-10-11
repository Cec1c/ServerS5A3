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
			state := &semesterSQLState{scenario: mode}
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
			if state.begins != 1 || state.writes == 0 {
				t.Fatal(state, e)
			}
			if mode == "success" {
				if e != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal(state, e)
				}
			} else if mode == "commit-unknown" {
				if e == nil || state.commits != 1 {
					t.Fatal(state, e)
				}
			} else if e == nil || state.commits != 0 || state.rollbacks != 1 {
				t.Fatal(state, e)
			}
		})
	}
}
