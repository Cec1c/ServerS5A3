package mysql

import (
	"context"
	"database/sql"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

func TestMySQLRaidRewardMailboxOnePhysicalTransaction(t *testing.T) {
	for _, mode := range []string{"success", "foreign_owner", "account_scope", "character_scope", "inventory_scope", "equipment_scope", "mail_scope", "late_abort", "write_failure", "commit_failure", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			state := &accountMailSQLState{scenario: mode}
			db := sql.OpenDB(accountMailSQLConnector{state})
			defer db.Close()
			u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			panicked := false
			var e error
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				e = u.WithinAccountCharacterMailboxAssets(ctx, "a", "19", func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, eq repo.EquipmentRepository, m repo.MailboxRepository) error {
					if mode == "account_scope" {
						_, _, e := a.Load(ctx, "b")
						return e
					}
					if mode == "character_scope" {
						_, _, e := c.Load(ctx, "20")
						return e
					}
					if mode == "inventory_scope" {
						_, _, e := i.Load(ctx, "20")
						return e
					}
					if mode == "equipment_scope" {
						_, _, e := eq.Load(ctx, "20")
						return e
					}
					if mode == "mail_scope" {
						_, _, e := m.Load(ctx, "20")
						return e
					}
					if e := a.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:321": {ItemID: 4, Count: 2}}}); e != nil {
						return e
					}
					ch, found, e := c.Load(ctx, "19")
					if e != nil || !found {
						return errors.New("owned character unavailable")
					}
					ch.Stats = map[string]int64{"receipt": 1}
					if e = repo.SaveCharacterFields(ctx, c, ch, repo.CharacterFieldStats); e != nil {
						return e
					}
					if e = i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:50": {ItemID: 10096296, Count: 1}}}); e != nil {
						return e
					}
					if _, e = repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "19", Source: "raid_reward_test", Attachments: []repo.MailAttachment{{ItemID: 10304483, Count: 1}}}); e != nil {
						return e
					}
					if mode == "late_abort" {
						return errors.New("late apply failure")
					}
					if mode == "panic" {
						panic("apply panic")
					}
					if mode == "cancel" {
						cancel()
					}
					return nil
				})
			}()
			if state.begins != 1 {
				t.Fatal("not one physical transaction", state.begins)
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") || !strings.HasSuffix(q, " FOR UPDATE") {
					t.Fatal("read outside locked write transaction", q)
				}
			}
			if mode == "success" {
				if e != nil || state.commits != 1 || state.rollbacks != 0 || state.writes < 4 {
					t.Fatalf("missing common commit err=%v state=%+v", e, state)
				}
				return
			}
			if e == nil && !panicked {
				t.Fatal("invalid transaction accepted")
			}
			if mode == "commit_failure" {
				if state.commits != 1 {
					t.Fatal("commit failure not reached")
				}
				return
			}
			if state.commits != 0 || state.rollbacks != 1 {
				t.Fatalf("rollback boundary err=%v state=%+v", e, state)
			}
		})
	}
}
