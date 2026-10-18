package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type equipmentSQLConnector struct{ s *accountMailSQLState }

func (c equipmentSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return equipmentSQLConn{accountMailSQLConn{c.s}}, nil
}
func (c equipmentSQLConnector) Driver() driver.Driver { return accountMailSQLDriver{c.s} }

type equipmentSQLConn struct{ accountMailSQLConn }

func (c equipmentSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, e := c.accountMailSQLConn.QueryContext(ctx, q, args)
	if e != nil {
		return nil, e
	}
	if strings.HasPrefix(q, "SELECT character_id, account_id,") || strings.HasPrefix(q, "SELECT character_id, updated_at") {
		v := rows.(*pairSQLRows)
		v.rows[0][0] = args[0].Value
	}
	return rows, nil
}
func TestMySQLEquipmentServicesOneTransactionAcrossOwners(t *testing.T) {
	for _, scenario := range []string{"success", "foreign_owner", "account_missing", "callback_failure", "write_failure", "commit_failure", "panic", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			s := &accountMailSQLState{scenario: scenario}
			db := sql.OpenDB(equipmentSQLConnector{s})
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
				err = u.WithinEquipmentServiceAssets(ctx, "a", "19", "42", func(g r.Group, source, dest r.CharacterRecord) error {
					if source.CharacterID != "19" || dest.CharacterID != "42" {
						t.Fatal(source, dest)
					}
					if _, _, e := g.Account.Load(ctx, "b"); e == nil {
						t.Fatal("跨账号读取放行")
					}
					if _, _, e := g.Inventory.Load(ctx, "42"); e == nil {
						t.Fatal("接收者背包越权读取")
					}
					if _, _, e := g.Mailbox.Load(ctx, "19"); e == nil {
						t.Fatal("邮箱越权读取")
					}
					if e := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{}}); e != nil {
						return e
					}
					if e := g.AccountInventory.Save(ctx, r.AccountInventoryRecord{AccountID: "a", Slots: map[string]r.ItemStack{}}); e != nil {
						return e
					}
					account, _, e := g.Account.Load(ctx, "a")
					if e != nil {
						return e
					}
					if e = r.SaveAccountMetadataEntry(ctx, g.Account, account, "epic_piece:1", "0", time.Now()); e != nil {
						return e
					}
					if _, e = r.AppendSystemMail(ctx, g.Mailbox, r.SystemMailDelivery{RecipientCharacterID: "42", Source: "跨界事务回归", Attachments: []r.MailAttachment{{ItemID: 1001, Count: 1}}}); e != nil {
						return e
					}
					switch scenario {
					case "callback_failure":
						return errors.New("回调失败")
					case "panic":
						panic("回调异常")
					case "cancel":
						cancel()
					}
					return nil
				})
			}()
			if scenario == "success" {
				if err != nil || s.commits != 1 || s.begins != 1 {
					t.Fatal(err, s)
				}
			} else if scenario == "panic" {
				if !panicked || s.commits != 0 || s.rollbacks == 0 {
					t.Fatal(s)
				}
			} else if err == nil || scenario != "commit_failure" && s.commits != 0 || scenario != "commit_failure" && s.rollbacks == 0 {
				t.Fatal(err, s)
			}
			for _, q := range s.queries {
				if strings.Contains(q, "readonly") || strings.HasPrefix(q, "SELECT ") && !strings.Contains(q, "FOR UPDATE") {
					t.Fatal("事务使用了非锁定或只读库查询", q)
				}
			}
		})
	}
}
