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

func TestHongbaoPurchaseUsesNativeSecondPrecision(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	now = now.Add(123456789 * time.Nanosecond)
	result, err := o.Purchase(context.Background(), HongbaoPurchaseRequest{hongbaoRequest(1, "fractional"), 103941}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(now.Add(10*24*time.Hour).Unix(), 0).UTC()
	inv, _, _ := repos.Inventory.Load(context.Background(), "1")
	if !result.ExpireAt.Equal(want) || !inv.Slots[result.Slot].ExpireAt.Equal(want) {
		t.Fatal(result.ExpireAt, inv)
	}
}

func TestHongbaoMySQLAtomicConcurrentAndRestart(t *testing.T) {
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
	name := fmt.Sprintf("hongbao_test_%d", time.Now().UnixNano())
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
	seed, _, now := hongbaoGroup(t)
	repos := newGroup()
	for i := 1; i <= 32; i++ {
		id := fmt.Sprint(i)
		actor, _, err := seed.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		account, _, err := seed.Account.Load(ctx, actor.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repos.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := repos.Character.Save(ctx, actor); err != nil {
			t.Fatal(err)
		}
		inv, found, err := seed.Inventory.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
		}
	}
	reg, _, err := seed.Settings.Load(ctx, RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, reg); err != nil {
		t.Fatal(err)
	}
	contract := hongbaoFixtureContract(t)
	newOwner := func() *HongbaoOwner {
		o, err := NewHongbaoOwner(newGroup(), contract)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	rollback := func(label string, operation func() error) {
		t.Helper()
		before := hongbaoSnapshot(t, repos)
		if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_hongbao BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test late hongbao save'"); err != nil {
			t.Fatal(err)
		}
		err := operation()
		if _, dropErr := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_hongbao"); dropErr != nil {
			t.Fatal(dropErr)
		}
		if err == nil {
			t.Fatal(label, "late failure accepted")
		}
		if !reflect.DeepEqual(before, hongbaoSnapshot(t, newGroup())) {
			t.Fatal(label, "SQL rollback changed assets")
		}
	}
	buy := HongbaoPurchaseRequest{hongbaoRequest(1, "sql-buy"), 103941}
	rollback("purchase", func() error { _, err := newOwner().Purchase(ctx, buy, now); return err })
	purchase, err := newOwner().Purchase(ctx, buy, now)
	if err != nil {
		t.Fatal(err)
	}
	issueReq := HongbaoIssueRequest{hongbaoRequest(1, "sql-issue"), purchase.Product.ItemID, 5}
	rollback("issue", func() error { _, err := newOwner().Issue(ctx, issueReq, now); return err })
	issue, err := newOwner().Issue(ctx, issueReq, now)
	if err != nil {
		t.Fatal(err)
	}
	claimReq := HongbaoClaimRequest{hongbaoRequest(1, "sql-claim"), issue.RecordID}
	rollback("claim", func() error { _, err := newOwner().Claim(ctx, claimReq, now); return err })
	first, err := newOwner().Claim(ctx, claimReq, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 4; i++ {
		hongbaoClaim(t, newOwner(), now, issue.RecordID, i)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 5; i <= 6; i++ {
		o := newOwner()
		wg.Add(1)
		go func(who int) {
			defer wg.Done()
			<-start
			_, err := o.Claim(ctx, HongbaoClaimRequest{hongbaoRequest(who, "last"), issue.RecordID}, now)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrHongbaoUnavailable) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("last share winners", winners)
	}
	rollback("settlement", func() error { _, err := newOwner().Settle(ctx, issue.RecordID, now); return err })
	settled, err := newOwner().Settle(ctx, issue.RecordID, now)
	if err != nil || settled.Coins != 40 || len(settled.MailIDs) != 1 {
		t.Fatal(settled, err)
	}
	before := hongbaoSnapshot(t, newGroup())
	claimAgain, err := newOwner().Claim(ctx, claimReq, now)
	if err != nil || !claimAgain.Replayed || claimAgain.Amount != first.Amount {
		t.Fatal(claimAgain, err)
	}
	settledAgain, err := newOwner().Settle(ctx, issue.RecordID, now)
	if err != nil || !settledAgain.Replayed || !reflect.DeepEqual(settled.MailIDs, settledAgain.MailIDs) {
		t.Fatal(settledAgain, err)
	}
	if !reflect.DeepEqual(before, hongbaoSnapshot(t, newGroup())) {
		t.Fatal("restart replay mutated assets")
	}
}
