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

func TestGuildAppearanceMySQLAtomicSave(t *testing.T) {
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
	name := fmt.Sprintf("guild_appearance_test_%d", time.Now().UnixNano())
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
	reg.Guilds[1].Mileage = 4000
	if err := Save(ctx, repos.Settings, reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	change := AppearanceChange{Owner: req.Creator, Mark: 5, Revalidate: func() bool { return true }}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	trigger := "CREATE TRIGGER `" + name + "`.reject_appearance BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test appearance failure'"
	if _, err = db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if id, e := ChangeAppearance(ctx, repos.Guild, change, appearanceCatalog(), time.Now()); e == nil || id != 0 {
		t.Fatal("SQL failure accepted")
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_appearance"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := newGroup().Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("SQL failure charged")
	}
	stale := change
	calls := 0
	stale.Revalidate = func() bool { calls++; return calls < 3 }
	if id, e := ChangeAppearance(ctx, repos.Guild, stale, appearanceCatalog(), time.Now()); e == nil || id != 0 {
		t.Fatal("stale session committed")
	}
	after, _, _ = newGroup().Settings.Load(ctx, RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("post-save session failure committed")
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for range 2 {
		group := newGroup()
		go func() {
			<-start
			_, e := ChangeAppearance(ctx, group.Guild, change, appearanceCatalog(), time.Now())
			results <- e
		}()
	}
	close(start)
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	reg, err = Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].Mark != 5 || reg.Guilds[1].Mileage != 2000 {
		t.Fatal("concurrent same-value retry charged twice", err)
	}
	change.Aura, change.AuraMark, change.AuraFrame = true, 1, 2
	if _, err := ChangeAppearance(ctx, repos.Guild, change, appearanceCatalog(), time.Now()); err != nil {
		t.Fatal(err)
	}
	change.AuraMark, change.AuraFrame = NoGuildAura, NoGuildAura
	if _, err := ChangeAppearance(ctx, repos.Guild, change, appearanceCatalog(), time.Now()); err != nil {
		t.Fatal(err)
	}
	reg, err = Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].AuraMark != NoGuildAura || reg.Guilds[1].AuraFrame != NoGuildAura || reg.Guilds[1].Mileage != 2000 {
		t.Fatal("clear or fee lost on reconnect", err)
	}
}
