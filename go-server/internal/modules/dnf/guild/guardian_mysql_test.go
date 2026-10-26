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

func TestGuardianExistingStateMySQLIncomeRollbackAndReconnect(t *testing.T) {
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
	name := fmt.Sprintf("guardian_existing_test_%d", time.Now().UnixNano())
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
	reg, g, owner, _ := textRegistry(t)
	g.Guardians = guardianExistingStateFixture()
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err = Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeChar, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	catalog := essenceTestCatalog(t)
	// A genuine SQL settings-save failure preserves ownership and day markers.
	if _, err = db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_guardian_save BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test guardian save failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err = GrantEssenceLogin(ctx, repos.Guild, owner, catalog, now, func() bool { return true }); err == nil {
		t.Fatal("save failure accepted")
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_guardian_save"); err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		after, _, err := newGroup().Settings.Load(ctx, RegistryScope)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("failed income changed existing ownership", err)
		}
		afterChar, _, err := newGroup().Character.Load(ctx, owner.CharacterID)
		if err != nil || !reflect.DeepEqual(beforeChar, afterChar) {
			t.Fatal("failed income changed daily record", err)
		}
	}
	assertUnchanged()
	calls := 0
	if _, err = GrantEssenceLogin(ctx, repos.Guild, owner, catalog, now, func() bool { calls++; return calls < 2 }); err == nil {
		t.Fatal("late session loss accepted")
	}
	assertUnchanged()
	results := make(chan error, 4)
	for range 4 {
		group := newGroup()
		go func() {
			_, err := GrantEssenceLogin(ctx, group.Guild, owner, catalog, now, func() bool { return true })
			results <- err
		}()
	}
	for range 4 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(ctx, newGroup().Settings)
	if err != nil || !reflect.DeepEqual(loaded.Guilds[1].Guardians, guardianExistingStateFixture()) || loaded.Guilds[1].Essence.AP != 10 {
		t.Fatal("reconnect after concurrent income", err)
	}
}
