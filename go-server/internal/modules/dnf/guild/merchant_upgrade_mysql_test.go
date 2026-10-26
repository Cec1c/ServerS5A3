package guild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// This test creates and drops only a unique disposable database. Without an
// explicitly isolated DSN it skips rather than touching a configured server.
func TestMerchantUpgradeMySQLAtomicRollbackConcurrencyAndReconnect(t *testing.T) {
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
	name := fmt.Sprintf("merchant_upgrade_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	newGroup := func() r.Group {
		group, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return group
	}
	repos := newGroup()
	if err = repos.Account.Save(ctx, r.AccountRecord{AccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长", Level: 90, Stats: map[string]int64{"gold": 900000}}); err != nil {
		t.Fatal(err)
	}
	q, cat, now := merchantUpgradeFixture(t, repos, [2]uint8{0, 0})

	for _, mode := range []string{"settings_save", "after_save"} {
		t.Run(mode, func(t *testing.T) {
			before, _, err := repos.Settings.Load(ctx, RegistryScope)
			if err != nil {
				t.Fatal(err)
			}
			request := q
			if mode == "settings_save" {
				if _, err = db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_merchant_upgrade BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test merchant upgrade save failure'"); err != nil {
					t.Fatal(err)
				}
			} else {
				calls := 0
				request.Revalidate = func() bool { calls++; return calls < 3 }
			}
			result, err := ApplyMerchantUpgrade(ctx, repos.Guild, request, cat, now)
			if mode == "settings_save" {
				if _, dropErr := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_merchant_upgrade"); dropErr != nil {
					t.Fatal(dropErr)
				}
			}
			if err == nil || result.GuildID != 0 {
				t.Fatalf("accepted failed write: %+v %v", result, err)
			}
			after, _, err := newGroup().Settings.Load(ctx, RegistryScope)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("SQL rollback err=%v", err)
			}
		})
	}

	t.Run("concurrent_separate_connections", func(t *testing.T) {
		groups := []r.Group{newGroup(), newGroup()}
		var wg sync.WaitGroup
		results := make(chan error, 8)
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				request := q
				request.RequestID = fmt.Sprintf("%064x", i+1)
				request.NPCID = []uint32{390, 389}[i%2]
				_, err := ApplyMerchantUpgrade(ctx, groups[i%2].Guild, request, cat, now)
				results <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			} else if !errors.Is(err, ErrMerchantStaleLevel) {
				t.Fatalf("concurrent error=%v", err)
			}
		}
		reg, err := Load(ctx, newGroup().Settings)
		if err != nil || success != 2 || len(reg.MerchantUpgradeReceipts) != 2 ||
			reg.Guilds[q.GuildID].Essence != (EssenceBalance{AP: 298000, GP: 20000}) ||
			reg.Guilds[q.GuildID].Merchants.Levels != [2]uint8{2, 2} {
			t.Fatalf("success=%d registry=%+v err=%v", success, reg, err)
		}
		for key, receipt := range reg.MerchantUpgradeReceipts {
			request := q
			request.RequestID = key[len(merchantUpgradeKeyPrefix):]
			request.NPCID = receipt.NPCID
			request.CurrentLevel = uint32(receipt.FromLevel)
			request.TargetLevel = uint32(receipt.ToLevel)
			replayed, err := ApplyMerchantUpgrade(ctx, newGroup().Guild, request, cat, now.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(replayed, receipt) {
				t.Fatalf("reconnect receipt=%+v err=%v", replayed, err)
			}
		}
	})
}
