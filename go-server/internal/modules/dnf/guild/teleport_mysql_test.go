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

func TestGuildTeleportMySQLFourOwnersRollbackReplayAndReconnect(t *testing.T) {
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
	name := fmt.Sprintf("guild_teleport_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	group := func() r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	repos := group()
	fixture, q, now := teleportFixture(t, 4)
	for _, p := range q.Participants {
		if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: p.Owner.AccountID}); err != nil {
			t.Fatal(err)
		}
		c, _, _ := fixture.Character.Load(ctx, p.Owner.CharacterID)
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	settings, _, _ := fixture.Settings.Load(ctx, RegistryScope)
	if err := repos.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_teleport BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test teleport receipt failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitTeleport(ctx, repos.Guild, q, now); err == nil {
		t.Fatal("failed registry save committed")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_teleport"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := group().Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed registry committed")
	}
	for _, p := range q.Participants {
		c, _, err := group().Character.Load(ctx, p.Owner.CharacterID)
		if err != nil || CharacterTeleportLocation(c) != p.From {
			t.Fatal("partial SQL move", err)
		}
	}
	start, results := make(chan struct{}), make(chan error, 8)
	for range 8 {
		g := group()
		go func() { <-start; _, err := CommitTeleport(ctx, g.Guild, q, now); results <- err }()
	}
	close(start)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	reg, err := Load(ctx, group().Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
		t.Fatal("repeat charge", err)
	}
	for _, p := range q.Participants {
		c, _, err := group().Character.Load(ctx, p.Owner.CharacterID)
		if err != nil || CharacterTeleportLocation(c) != q.Destination {
			t.Fatal("SQL destination missing", err)
		}
	}
	if _, err := CommitTeleport(ctx, group().Guild, q, now.Add(time.Hour)); err != nil {
		t.Fatal("reconnect receipt", err)
	}
}
