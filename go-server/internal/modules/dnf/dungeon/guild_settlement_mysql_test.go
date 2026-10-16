package dungeon

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

func TestGuildSettlementMySQLAtomicConcurrentAndRestart(t *testing.T) {
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
		t.Fatal("open disposable database")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("guild_clear_test_%d", time.Now().UnixNano())
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
	g := newGroup()
	fixture, cmd := guildSettlementFixture(t)
	account, _, _ := fixture.Account.Load(ctx, cmd.AccountID)
	character, _, _ := fixture.Character.Load(ctx, cmd.CharacterID)
	skill, _, _ := fixture.Skill.Load(ctx, cmd.CharacterID)
	inv, _, _ := fixture.Inventory.Load(ctx, cmd.CharacterID)
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	reg := guild.NewRegistry()
	for i := 1; i <= 2; i++ {
		id := fmt.Sprint(i)
		character.CharacterID, character.Name, character.Slot = id, "Clear"+id, i-1
		skill.CharacterID, inv.CharacterID = id, id
		for _, err := range []error{g.Character.Save(ctx, character), g.Skill.Save(ctx, skill), g.Inventory.Save(ctx, inv)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 1 {
			_, err = reg.Create(character, "ClearGuild", cmd.UpdatedAt)
		} else {
			_, err = reg.JoinDirect(1, character, cmd.UpdatedAt)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := guild.Save(ctx, g.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	cmd.CharacterID = "1"
	beforeC, _, _ := g.Character.Load(ctx, "1")
	beforeS, _, _ := g.Skill.Load(ctx, "1")
	beforeG, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
	// Fails at the final receipt write, AFTER the guild, EXP, SP and account
	// changes have executed in this real SQL transaction.
	trigger := "CREATE TRIGGER `" + name + "`.reject_clear BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW BEGIN IF NEW.scope LIKE 'guild-clear:v1:%' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='late receipt failure'; END IF; END"
	if _, err := db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	o, _ := NewOwner(g)
	if _, err := o.CommitSettlement(ctx, cmd); err == nil {
		t.Fatal("late SQL failure succeeded")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_clear"); err != nil {
		t.Fatal(err)
	}
	fresh := newGroup()
	afterC, _, _ := fresh.Character.Load(ctx, "1")
	afterS, _, _ := fresh.Skill.Load(ctx, "1")
	afterG, _, _ := fresh.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeS, afterS) || !reflect.DeepEqual(beforeG, afterG) {
		t.Fatal("SQL failure retained partial award")
	}
	if _, found, err := fresh.Settings.Load(ctx, r.GuildSettlementReceiptScope(r.GuildOwner{AccountID: cmd.AccountID, CharacterID: "1"}, cmd.CompletionKey)); err != nil || found {
		t.Fatal("failed receipt remained")
	}
	start, results := make(chan struct{}), make(chan error, 4)
	for i := 0; i < 4; i++ {
		group := newGroup()
		c := cmd
		c.CharacterID = fmt.Sprint(i%2 + 1)
		go func() { <-start; owner, _ := NewOwner(group); _, err := owner.CommitSettlement(ctx, c); results <- err }()
	}
	close(start)
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	reg, err = guild.Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].Mileage != 20 {
		t.Fatalf("concurrent mileage=%v err=%v", reg.Guilds[1], err)
	}
	o, _ = NewOwner(newGroup())
	second := cmd
	second.CompletionKey = "another-clear"
	if _, err := o.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	o, _ = NewOwner(newGroup())
	replayed, err := o.CommitSettlement(ctx, cmd)
	reg, _ = guild.Load(ctx, newGroup().Settings)
	if err != nil || !replayed.Replayed || reg.Guilds[1].Mileage != 30 {
		t.Fatalf("restart old replay=%+v err=%v", replayed, err)
	}
}
