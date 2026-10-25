package guild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestGuardianTransactionMySQLRollbackConcurrencyAndReconnect(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("guardian_transaction_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	newGroup := func() r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	repos := newGroup()
	if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长", Level: 90, Stats: map[string]int64{"gold": 900000}}); err != nil {
		t.Fatal(err)
	}
	q, cat, now := guardianTransactionSeed(t, repos)
	q.Policy.FirstPurchase = GuardianPaid
	for _, mode := range []string{"settings_save", "after_save"} {
		t.Run(mode, func(t *testing.T) {
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			request := q
			if mode == "settings_save" {
				if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_guardian_transaction BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test guardian transaction save failure'"); err != nil {
					t.Fatal(err)
				}
			} else {
				calls := 0
				request.Revalidate = func() bool { calls++; return calls < 3 }
			}
			result, err := ApplyGuardian(ctx, repos.Guild, request, cat, now)
			if mode == "settings_save" {
				if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_guardian_transaction"); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil || result.GuildID != 0 {
				t.Fatal(result, err)
			}
			after, _, err := newGroup().Settings.Load(ctx, RegistryScope)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("SQL rollback", err)
			}
		})
	}
	for _, mode := range []string{"same_receipt", "same_id", "two_ids"} {
		t.Run(mode, func(t *testing.T) {
			q, cat, now := guardianTransactionSeed(t, repos)
			guardianConcurrentPurchases(t, repos, q, cat, now, mode)
			// Rebuild repository owners as after restart, recover original durable
			// purchase receipt, and select through the production domain owner.
			reg, err := Load(ctx, newGroup().Settings)
			if err != nil {
				t.Fatal(err)
			}
			for key, receipt := range reg.GuardianReceipts {
				request := q
				request.RequestID = key[len("guardian:"):]
				request.GuardianID = receipt.Guardian.ID
				got, err := ApplyGuardian(ctx, newGroup().Guild, request, cat, now.Add(time.Hour))
				if err != nil || !reflect.DeepEqual(got, receipt) {
					t.Fatal("reconnect receipt", got, err)
				}
			}
			q.Action = GuardianSelect
			q.GuardianID = reg.Guilds[1].Guardians.Owned[0].ID
			q.RequestID = fmt.Sprintf("%064x", 99)
			before := reg.Guilds[1].Essence
			result, err := ApplyGuardian(ctx, newGroup().Guild, q, cat, now)
			if err != nil || result.Cost != (EssenceBalance{}) {
				t.Fatal(result, err)
			}
			after, err := Load(ctx, newGroup().Settings)
			if err != nil || after.Guilds[1].Essence != before || after.Guilds[1].Guardians.ActiveID != q.GuardianID {
				t.Fatal("selection", err)
			}
		})
	}
}
