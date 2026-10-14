package guild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// Creates/drops only a unique database on an explicitly disposable server.
func TestGuildMySQLAtomicCreationAndRegistryLock(t *testing.T) {
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
	name := fmt.Sprintf("guild_test_%d", time.Now().UnixNano())
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
	g := newGroup()
	for i := 1; i <= 8; i++ {
		id := fmt.Sprint(i)
		account := "a" + id
		if e := g.Account.Save(ctx, r.AccountRecord{AccountID: account}); e != nil {
			t.Fatal(e)
		}
		if e := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: account, Name: "角色" + id, Level: 90, Stats: map[string]int64{"gold": 900000, "sp": 123}}); e != nil {
			t.Fatal(e)
		}
	}
	// Separate repository instances share no Go mutex. The first creation must
	// serialize even while the registry row does not yet exist.
	groups := []r.Group{newGroup(), newGroup()}
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, e := CreateWithFoundingMember(ctx, groups[i].Guild, guildRequest(i*2+1, i*2+2, "同名公会", i+1), time.Now())
			results <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if e != ErrNameExists {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("successful competing creations=%d", successes)
	}
	fresh := newGroup()
	reg, err := Load(ctx, fresh.Settings)
	if err != nil || len(reg.Guilds) != 1 || len(reg.Creations) != 1 {
		t.Fatalf("reload=%+v err=%v", reg, err)
	}
	gold := int64(0)
	for _, id := range []string{"1", "3"} {
		c, found, e := fresh.Character.Load(ctx, id)
		if e != nil || !found || c.Stats["sp"] != 123 {
			t.Fatalf("reload character %s: %v", id, e)
		}
		gold += c.Stats["gold"]
	}
	if gold != 1500000 {
		t.Fatalf("competing debit sum=%d", gold)
	}
	for operation, receipt := range reg.Creations {
		result, e := CreateWithFoundingMember(ctx, fresh.Guild, CreateRequest{OperationID: operation, Creator: receipt.Creator, Founder: receipt.Founder, Name: receipt.Name, Promotion: receipt.Promotion}, time.Now())
		if e != nil || !result.Replay {
			t.Fatalf("durable retry=%+v err=%v", result, e)
		}
	}
	// Fail the actual SQL statement after gold has been written. Neither the
	// debit, registry, nor receipt may survive that storage failure.
	trigger := "CREATE TRIGGER `" + name + "`.reject_guild_state BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test late guild failure'"
	if _, err = db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if _, err = CreateWithFoundingMember(ctx, g.Guild, guildRequest(5, 6, "回滚公会", 3), time.Now()); err == nil {
		t.Fatal("expected late SQL failure")
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_guild_state"); err != nil {
		t.Fatal(err)
	}
	c, _, err := fresh.Character.Load(ctx, "5")
	if err != nil || c.Stats["gold"] != 900000 {
		t.Fatalf("rollback gold=%v err=%v", c.Stats, err)
	}
	reg, err = Load(ctx, fresh.Settings)
	if err != nil || len(reg.Guilds) != 1 || len(reg.Creations) != 1 {
		t.Fatalf("rollback registry=%+v err=%v", reg, err)
	}
	if _, err = CreateWithFoundingMember(ctx, g.Guild, guildRequest(5, 6, "回滚公会", 3), time.Now()); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}
