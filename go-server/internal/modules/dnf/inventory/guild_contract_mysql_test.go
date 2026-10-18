package inventory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestGuildContractMySQLAtomicPurchaseAndClaim(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("guild_contract_test_%d", time.Now().UnixNano())
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
	seed, _, q, now := contractClaimFixture(t)
	repos := newGroup()
	actor, _, _ := seed.Character.Load(ctx, "1")
	actor.Stats["gold"] = 10000000
	if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	bag, _, _ := seed.Inventory.Load(ctx, "1")
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(ctx, seed.Settings)
	reg.Guilds[1].Benefits = nil
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.Catalog.Contents = map[uint32]guild.Content{23: {ID: 23, Type: 21, Value: 300, Days: 1, Gold: true, Price: 3000000}}
	purchase := guild.CargoPurchase{Owner: q.Owner, ContentID: 23, RequestID: fmt.Sprintf("%064x", 100), Revalidate: q.Revalidate}
	trigger := "CREATE TRIGGER `" + name + "`.reject_contract BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected contract failure'"
	actor, _, _ = repos.Character.Load(ctx, "1")
	bag, _, _ = repos.Inventory.Load(ctx, "1")
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if _, err := db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if got, err := guild.PurchaseBenefit(ctx, repos.Guild, purchase, q.Catalog, now); err == nil || got.GuildID != 0 {
		t.Fatal("SQL failure purchased", got, err)
	}
	after, _, _ := newGroup().Settings.Load(ctx, guild.RegistryScope)
	current, _, _ := newGroup().Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(actor, current) {
		t.Fatal("SQL purchase failure lost gold")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_contract"); err != nil {
		t.Fatal(err)
	}
	start, done := make(chan struct{}), make(chan error, 6)
	for range 6 {
		group := newGroup()
		go func() {
			<-start
			_, err := guild.PurchaseBenefit(ctx, group.Guild, purchase, q.Catalog, now)
			done <- err
		}()
	}
	close(start)
	for range 6 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	current, _, _ = newGroup().Character.Load(ctx, "1")
	reg, err = guild.Load(ctx, newGroup().Settings)
	if err != nil || current.Stats["gold"] != 7000000 || len(reg.BenefitPurchases) != 1 {
		t.Fatal("SQL duplicate purchase", err)
	}
	before, _, _ = repos.Settings.Load(ctx, guild.RegistryScope)
	if _, err := db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := owner.ClaimGuildContract(ctx, q, now); err == nil || got.GuildID != 0 {
		t.Fatal("SQL failed claim succeeded", got, err)
	}
	after, _, _ = newGroup().Settings.Load(ctx, guild.RegistryScope)
	currentBag, _, _ := newGroup().Inventory.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bag, currentBag) {
		t.Fatal("SQL claim failure kept item or receipt")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_contract"); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	for range 6 {
		worker, err := NewOwner(newGroup())
		if err != nil {
			t.Fatal(err)
		}
		go func() { <-start; _, err := worker.ClaimGuildContract(ctx, q, now); done <- err }()
	}
	close(start)
	for range 6 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	reg, err = guild.Load(ctx, newGroup().Settings)
	currentBag, _, _ = newGroup().Inventory.Load(ctx, "1")
	if err != nil || len(reg.ContractClaims) != 1 || currentBag.Slots["0:88"].Count != 1 || currentBag.Slots["0:89"].Count != 0 {
		t.Fatal("SQL duplicate claim", err)
	}
	fresh, err := NewOwner(newGroup())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ClaimGuildContract(ctx, q, now.Add(24*time.Hour)); err != nil {
		t.Fatal("old request reconciliation", err)
	}
	q.RequestID = fmt.Sprintf("%064x", 2)
	if _, err := fresh.ClaimGuildContract(ctx, q, now.Add(time.Hour)); err != guild.ErrContractAlreadyClaimed {
		t.Fatal("new connection granted twice", err)
	}
	if _, err := guild.PurchaseBenefit(ctx, newGroup().Guild, guild.CargoPurchase{Owner: q.Owner, ContentID: 23, RequestID: fmt.Sprintf("%064x", 1), Revalidate: q.Revalidate}, q.Catalog, now); err != guild.ErrPurchaseIdentity {
		t.Fatal("claim token reused for gold purchase", err)
	}
}
