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
	testGuildSettlementMySQLAtomicConcurrentAndRestart(t, false)
}

func TestGuildRankingMySQLAtomicConcurrentAndRestart(t *testing.T) {
	testGuildSettlementMySQLAtomicConcurrentAndRestart(t, true)
}

func testGuildSettlementMySQLAtomicConcurrentAndRestart(t *testing.T, ranking bool) {
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
	cmd.GuildEssence, err = guild.ParseEssenceCatalog("[daily max ap]\n5000\n[ap reward]\n10 15 15 30 0\n[/ap reward]")
	if err != nil {
		t.Fatal(err)
	}
	cmd.GuildEssenceRevalidate = func() bool { return true }
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
	if ranking {
		cmd.GuildRanking = &guild.RankingPartyClear{DungeonID: 247, StartedAt: cmd.UpdatedAt,
			Members: []r.GuildOwner{{AccountID: cmd.AccountID, CharacterID: "1"}, {AccountID: cmd.AccountID, CharacterID: "2"}},
			Rules:   &guild.RankingRules{PartyClears: map[uint32]guild.RankingPoints{247: {Points: 5}}}, Revalidate: func() bool { return true }}
	}
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
	if err != nil || reg.Guilds[1].Mileage != 20 || reg.Guilds[1].Essence.AP != 30 {
		t.Fatalf("concurrent mileage=%v err=%v", reg.Guilds[1], err)
	}
	if ranking && (reg.Guilds[1].RankingActivity == nil || reg.Guilds[1].RankingActivity.PartyClear != 10) {
		t.Fatal("ranking concurrency did not commit exactly two clears")
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
	if err != nil || !replayed.Replayed || reg.Guilds[1].Mileage != 30 || reg.Guilds[1].Essence.AP != 45 {
		t.Fatalf("restart old replay=%+v err=%v", replayed, err)
	}
	if history := reg.Guilds[1].MileageHistory; len(history) != 3 || history[0].Kind != guild.MileageHistoryDungeon || history[0].Amount != 10 {
		t.Fatalf("SQL dungeon history/replay=%+v", history)
	}
	if ranking {
		if reg.Guilds[1].RankingActivity.PartyClear != 15 || replayed.GuildRanking.Points != 5 {
			t.Fatal("ranking restart/replay lost durable result")
		}
		owner := r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}
		digest, err := guild.RankingSourceDigest(reg)
		if err != nil {
			t.Fatal(err)
		}
		period, err := guild.BuildRankingPeriod(cmd.UpdatedAt, map[uint32]guild.RankingFactors{1: {PartyClear: 15}})
		if err != nil {
			t.Fatal(err)
		}
		before, _, _ := fresh.Settings.Load(ctx, guild.RegistryScope)
		trigger := "CREATE TRIGGER `" + name + "`.reject_rank BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW BEGIN IF NEW.scope = '" + guild.RegistryScope + "' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='ranking publication failure'; END IF; END"
		if _, err := db.ExecContext(ctx, trigger); err != nil {
			t.Fatal(err)
		}
		if err := guild.PublishRanking(ctx, fresh.Guild, owner, digest, period); err == nil {
			t.Fatal("failed SQL ranking publication succeeded")
		}
		after, _, _ := newGroup().Settings.Load(ctx, guild.RegistryScope)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed SQL ranking publication changed snapshot")
		}
		if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_rank"); err != nil {
			t.Fatal(err)
		}
		if err := guild.PublishRanking(ctx, fresh.Guild, owner, digest, period); err != nil {
			t.Fatal(err)
		}
		loaded, err := guild.Load(ctx, newGroup().Settings)
		if err != nil || loaded.Ranking.View(1, cmd.UpdatedAt)[0].Score != 15 {
			t.Fatal("SQL ranking snapshot lost", err)
		}
	}
	// Login has its own character-day idempotency, shared with clear income.
	for range 2 {
		if _, err := guild.GrantEssenceLogin(ctx, newGroup().Guild, r.GuildOwner{AccountID: cmd.AccountID, CharacterID: "1"}, cmd.GuildEssence, cmd.UpdatedAt, func() bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	reg, err = guild.Load(ctx, newGroup().Settings)
	if err != nil || reg.Guilds[1].Essence.AP != 55 || reg.Guilds[1].Essence.GP != 0 {
		t.Fatal("login restart", err)
	}
}
