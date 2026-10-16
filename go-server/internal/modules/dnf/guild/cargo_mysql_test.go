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

func TestGuildCargoMySQLInitializationAndRollback(t *testing.T) {
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
	name := fmt.Sprintf("guild_cargo_test_%d", time.Now().UnixNano())
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
	created, err := CreateSolo(ctx, repos.Guild, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	query := CargoQuery{Owner: req.Creator, Revalidate: func() bool { return true }}
	policy := CargoInitialization{InitialCapacity: 8, MaxCapacity: 56, Now: time.Now()}
	before, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
	// Reject the actual SQL settings insert, after the initialization mutates
	// its transaction-local registry. A fresh connection must still see nil.
	trigger := "CREATE TRIGGER `" + name + "`.reject_cargo BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test cargo failure'"
	if _, err = db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if got, e := OpenCargo(ctx, repos.Guild, query, policy); e == nil || got.Cargo != nil {
		t.Fatalf("expected SQL failure: %+v %v", got, e)
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_cargo"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := newGroup().Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed initialization changed durable settings")
	}
	// Post-save session failure must also roll back successful SQL writes.
	calls := 0
	stale := query
	stale.Revalidate = func() bool { calls++; return calls < 3 }
	if got, e := OpenCargo(ctx, repos.Guild, stale, policy); e == nil || got.Cargo != nil {
		t.Fatal("late session failure accepted")
	}
	after, _, _ = newGroup().Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("late session failure did not roll back SQL")
	}
	// Independent repository groups share only the database row lock.
	start, results := make(chan struct{}), make(chan error, 2)
	for i := 0; i < 2; i++ {
		g := newGroup()
		go func() {
			<-start
			got, e := OpenCargo(ctx, g.Guild, query, policy)
			if e == nil && (got.Cargo == nil || got.Cargo.Capacity != 8) {
				e = fmt.Errorf("invalid initial warehouse")
			}
			results <- e
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	fresh := newGroup()
	reg, err := Load(ctx, fresh.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[created.Guild.ID]
	if g.Cargo == nil || g.Cargo.Capacity != 8 {
		t.Fatal("initialization missing after reconnect")
	}
	g.Cargo.Capacity = 24
	g.Cargo.Slots = map[uint16]r.ItemStack{23: {ItemID: 100, Count: 2, Extra: map[string]string{"serial": "keep"}}}
	if err = Save(ctx, fresh.Settings, reg, policy.Now); err != nil {
		t.Fatal(err)
	}
	before, _, _ = fresh.Settings.Load(ctx, r.GuildRegistryScope)
	policy.Now = policy.Now.Add(time.Hour)
	got, err := OpenCargo(ctx, newGroup().Guild, query, policy)
	if err != nil || got.Cargo.Capacity != 24 || got.Cargo.Slots[23].Count != 2 {
		t.Fatalf("expanded warehouse: %+v %v", got, err)
	}
	after, _, _ = fresh.Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("repeat open rewrote expanded warehouse")
	}
}
