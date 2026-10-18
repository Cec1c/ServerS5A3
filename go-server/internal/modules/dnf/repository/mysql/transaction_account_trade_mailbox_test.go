package mysql

import (
	"context"
	"database/sql"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

func TestMySQLAccountTradeMailboxAtomicScopes(t *testing.T) {
	for _, scenario := range []string{"success", "foreign_owner", "account_missing", "account_scope", "character_scope", "inventory_scope", "mail_scope", "callback_failure", "write_failure", "commit_failure", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			state := &accountMailSQLState{scenario: scenario}
			db := sql.OpenDB(accountMailSQLConnector{state})
			defer db.Close()
			u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = u.WithinAccountTradeMailbox(ctx, "a", []string{"19"}, "19", func(a r.AccountRepository, c r.CharacterRepository, i r.InventoryRepository, m r.MailboxRepository) error {
					switch scenario {
					case "account_scope":
						_, _, e := a.Load(ctx, "b")
						return e
					case "character_scope":
						_, _, e := c.Load(ctx, "20")
						return e
					case "inventory_scope":
						_, _, e := i.Load(ctx, "20")
						return e
					case "mail_scope":
						_, _, e := m.Load(ctx, "20")
						return e
					}
					account, _, e := a.Load(ctx, "a")
					if e != nil {
						return e
					}
					account.Metadata = map[string]string{"account_token_cera": "100"}
					if e = a.Save(ctx, account); e != nil {
						return e
					}
					if _, e = r.AppendSystemMail(ctx, m, r.SystemMailDelivery{RecipientCharacterID: "19", Source: "金币寄售", Gold: 100}); e != nil {
						return e
					}
					switch scenario {
					case "callback_failure":
						return errors.New("注入回调失败")
					case "panic":
						panic("注入panic")
					case "cancel":
						cancel()
					}
					return nil
				})
			}()
			if state.begins != 1 {
				t.Fatal(state)
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") || !strings.HasSuffix(q, " FOR UPDATE") {
					t.Fatal(q)
				}
			}
			if scenario == "success" {
				if err != nil || state.commits != 1 || state.rollbacks != 0 || state.writes < 2 {
					t.Fatal(err, state)
				}
				return
			}
			if err == nil && !panicked {
				t.Fatal("异常未拒绝")
			}
			if scenario != "commit_failure" && (state.commits != 0 || state.rollbacks != 1) {
				t.Fatal(err, state)
			}
		})
	}
}
