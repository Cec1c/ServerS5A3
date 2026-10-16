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

func TestGuildCargoPurchaseMySQLAtomicSave(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
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
	name := fmt.Sprintf("guild_purchase_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); e != nil {
			t.Error(e)
		}
	}()
	newGroup := func() r.Group {
		g, e := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	repos := newGroup()
	if err = repos.Account.Save(ctx, r.AccountRecord{AccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "仓库会员", Level: 90, Stats: map[string]int64{"gold": 900000}}); err != nil {
		t.Fatal(err)
	}
	req := guildRequest(1, 0, "数据库仓库", 91)
	req.Founder = r.GuildOwner{}
	_, err = CreateSolo(ctx, repos.Guild, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Mileage = 20000
	if err := Save(ctx, repos.Settings, reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	query := CargoPurchase{Owner: req.Creator, ContentID: 11, RequestID: fmt.Sprintf("%064x", 1), InitialCapacity: 8, Revalidate: func() bool { return true }}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	trigger := "CREATE TRIGGER `" + name + "`.reject_purchase BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test purchase failure'"
	if _, err = db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if got, e := PurchaseCargo(ctx, repos.Guild, query, cargoPurchaseCatalog(), time.Now()); e == nil || got.GuildID != 0 {
		t.Fatal("SQL failure accepted")
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_purchase"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := newGroup().Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("SQL failure committed")
	}
	stale := query
	calls := 0
	stale.Revalidate = func() bool { calls++; return calls < 3 }
	if got, e := PurchaseCargo(ctx, repos.Guild, stale, cargoPurchaseCatalog(), time.Now()); e == nil || got.GuildID != 0 {
		t.Fatal("post-save session loss committed")
	}
	after, _, _ = newGroup().Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("post-save failure charged")
	}
	start, results := make(chan struct{}), make(chan error, 4)
	for range 4 {
		group := newGroup()
		go func() {
			<-start
			_, e := PurchaseCargo(ctx, group.Guild, query, cargoPurchaseCatalog(), time.Now())
			results <- e
		}()
	}
	close(start)
	for range 4 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	reg, err = Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].Cargo.Capacity != 24 || reg.Guilds[1].Mileage != 10000 || len(reg.CargoPurchases) != 1 {
		t.Fatal("concurrent duplicate charged", err)
	}
	// A fresh repository/session can reconcile the same durable receipt.
	got, e := PurchaseCargo(ctx, newGroup().Guild, query, cargoPurchaseCatalog(), time.Now())
	if e != nil || got.After != 24 {
		t.Fatal("receipt recovery failed", e)
	}
	query.RequestID = fmt.Sprintf("%064x", 2)
	got, e = PurchaseCargo(ctx, newGroup().Guild, query, cargoPurchaseCatalog(), time.Now())
	if e != nil || got.After != 40 {
		t.Fatal("independent repeat blocked", e)
	}
	reg, err = Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].Cargo.Capacity != 40 || reg.Guilds[1].Mileage != 0 || len(reg.CargoPurchases) != 2 {
		t.Fatal("reconnect readback differs", err)
	}
}
