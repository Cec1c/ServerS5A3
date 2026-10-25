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

func TestGuildDungeonMySQLRewardsRollbackConcurrentAndReconnect(t *testing.T) {
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
		t.Fatal("open disposable database")
	}
	defer db.Close()
	name := fmt.Sprintf("guild_dungeon_test_%d", time.Now().UnixNano())
	ctx := context.Background()
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
	fixture, base := guildDungeonSettlementFixture(t)
	g := group()
	reg := guild.NewRegistry()
	ch, _, _ := fixture.Character.Load(ctx, base.CharacterID)
	sk, _, _ := fixture.Skill.Load(ctx, base.CharacterID)
	inv, _, _ := fixture.Inventory.Load(ctx, base.CharacterID)
	owners := []r.GuildOwner{{AccountID: "a", CharacterID: "1"}, {AccountID: "b", CharacterID: "2"}}
	for i, owner := range owners {
		ch.CharacterID, ch.AccountID, ch.Name, ch.Slot = owner.CharacterID, owner.AccountID, "Member"+owner.CharacterID, 0
		sk.CharacterID = owner.CharacterID
		inv.CharacterID = owner.CharacterID
		for _, err := range []error{g.Account.Save(ctx, r.AccountRecord{AccountID: owner.AccountID}), g.Character.Save(ctx, ch), g.Skill.Save(ctx, sk), g.Inventory.Save(ctx, inv)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			_, err = reg.Create(ch, "DungeonGuild", base.UpdatedAt)
		} else {
			_, err = reg.JoinDirect(1, ch, base.UpdatedAt)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = guild.Save(ctx, g.Settings, reg, base.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	entries, err := guild.PrepareDungeonEntries(reg, base.GuildDungeon.Catalog, 8500, owners, "sql-party-run", base.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]SettlementCommand, 2)
	for i, entry := range entries {
		cmd := base
		clear := *base.GuildDungeon
		clear.Entry = entry
		cmd.GuildDungeon = &clear
		cmd.AccountID = entry.Owner.AccountID
		cmd.CharacterID = entry.Owner.CharacterID
		cmd.CompletionKey = entry.CompletionKey()
		commands[i] = cmd
	}
	snapshot := func(g r.Group) []any {
		c, _, e := g.Character.Load(ctx, "1")
		if e != nil {
			t.Fatal(e)
		}
		s, _, e := g.Skill.Load(ctx, "1")
		if e != nil {
			t.Fatal(e)
		}
		i, _, e := g.Inventory.Load(ctx, "1")
		if e != nil {
			t.Fatal(e)
		}
		v, _, e := g.Settings.Load(ctx, guild.RegistryScope)
		if e != nil {
			t.Fatal(e)
		}
		return []any{c, s, i, v}
	}
	before := snapshot(g)
	trigger := "CREATE TRIGGER `" + name + "`.reject_guild_dungeon BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW BEGIN IF NEW.scope LIKE 'guild-clear:v1:%' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='late receipt failure'; END IF; END"
	if _, err = db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(g)
	if _, err = owner.CommitSettlement(ctx, commands[0]); err == nil {
		t.Fatal("late SQL failure committed")
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_guild_dungeon"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(group())) {
		t.Fatal("late failure partially committed inventory, contribution, purification, exp or skill")
	}
	errors := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		cmd := commands[i%2]
		g := group()
		go func() { <-start; o, _ := NewOwner(g); _, err := o.CommitSettlement(ctx, cmd); errors <- err }()
	}
	close(start)
	for i := 0; i < 8; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	fresh := group()
	reg, err = guild.Load(ctx, fresh.Settings)
	if err != nil || reg.Guilds[1].Dungeon.Counts[8500] != 2 {
		t.Fatal("lost or duplicated party count", err)
	}
	for _, id := range []string{"1", "2"} {
		inv, _, err := fresh.Inventory.Load(ctx, id)
		if err != nil || inv.Slots["0:121"].Count != 3 || reg.Guilds[1].Members[id].ContributionPoints != 20 {
			t.Fatal("bad award", id, inv, err)
		}
	}
	replayOwner, _ := NewOwner(group())
	replay, err := replayOwner.CommitSettlement(ctx, commands[0])
	if err != nil || !replay.Replayed || len(replay.GuildDungeonItems) != 1 {
		t.Fatal("durable replay", err)
	}
	next := time.Unix(int64(entries[0].Cycle.EndUnix), 0)
	stale := commands[0]
	clear := *stale.GuildDungeon
	clear.Entry.RunID = "uncommitted-old"
	stale.GuildDungeon = &clear
	stale.CompletionKey = clear.Entry.CompletionKey()
	stale.UpdatedAt = next
	if _, err = replayOwner.CommitSettlement(ctx, stale); err == nil {
		t.Fatal("old-cycle write allowed")
	}
	freshEntries, err := guild.PrepareDungeonEntries(reg, base.GuildDungeon.Catalog, 8500, owners, "new-cycle", next)
	if err != nil {
		t.Fatal(err)
	}
	clear.Entry = freshEntries[0]
	stale.CompletionKey = clear.Entry.CompletionKey()
	result, err := replayOwner.CommitSettlement(ctx, stale)
	if err != nil || result.GuildDungeon.Count != 1 || result.Inventory.Slots["0:121"].Count != 6 {
		t.Fatal("new-cycle clear", result, err)
	}
	replay, err = replayOwner.CommitSettlement(ctx, commands[0])
	if err != nil || !replay.Replayed {
		t.Fatal("old receipt after reset", err)
	}
	reg, err = guild.Load(ctx, group().Settings)
	if err != nil || reg.Guilds[1].Dungeon.Counts[8500] != 1 {
		t.Fatal("old receipt altered new-cycle count", err)
	}
}
