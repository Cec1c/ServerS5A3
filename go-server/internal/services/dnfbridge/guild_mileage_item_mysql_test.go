package dnfbridge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/inventory"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestGuildMileageItemMySQLAtomicAndConcurrent(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable test DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable database")
	}
	defer db.Close()
	peerDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open second disposable pool")
	}
	defer peerDB.Close()
	ctx := context.Background()
	prefix := fmt.Sprintf("guild_mileage_test_%d", time.Now().UnixNano())
	names := []string{prefix + "_a", prefix + "_b"}
	plan := r.DatabasePlan{ReadDatabases: names, WriteDatabases: names}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, name := range names {
			if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error(err)
			}
		}
	}()
	newGroup := func(pool *sql.DB) r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(pool, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	s, _, seed := guildMileageFixture(t)
	g := newGroup(db)
	for _, id := range []string{"29", "30"} {
		c, _, err := seed.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		inv, _, err := seed.Inventory.Load(ctx, "29")
		if err != nil {
			t.Fatal(err)
		}
		inv.CharacterID = id
		stack := inv.Slots["0:87"]
		stack.Count = 2
		inv.Slots["0:87"] = stack
		if err := g.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	state, _, err := seed.Settings.Load(ctx, dnfguild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Settings.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	def, err := s.pvfItemCatalog.ResolveItem(10153530)
	if err != nil {
		t.Fatal(err)
	}
	req := inventory.GuildMileageItemRequest{Owner: r.GuildOwner{CharacterID: "29", AccountID: "hall:29"}, SlotIndex: 87, Definition: *def.GuildMileageItem, Revalidate: func() bool { return true }}
	beforeInv, _, _ := g.Inventory.Load(ctx, "29")
	beforeReg, _, _ := g.Settings.Load(ctx, dnfguild.RegistryScope)
	for _, name := range names {
		if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.`reject_mileage` BEFORE INSERT ON `"+name+"`.`dnf_setting_values` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected late guild mileage save failure'"); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := inventory.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.UseGuildMileageItem(ctx, req, s.gameplayNow()); err == nil {
		t.Fatal("late SQL failure accepted")
	}
	fresh := newGroup(peerDB)
	gotInv, _, _ := fresh.Inventory.Load(ctx, "29")
	gotReg, _, _ := fresh.Settings.Load(ctx, dnfguild.RegistryScope)
	if !reflect.DeepEqual(gotInv, beforeInv) || !reflect.DeepEqual(gotReg, beforeReg) {
		t.Fatal("SQL failure lost item or changed guild")
	}
	for _, name := range names {
		if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.`reject_mileage`"); err != nil {
			t.Fatal(err)
		}
	}
	peerOwner, err := inventory.NewOwner(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var applied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		current := req
		worker := owner
		if i%2 == 1 {
			current.Owner = r.GuildOwner{CharacterID: "30", AccountID: "hall:30"}
			worker = peerOwner
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := worker.UseGuildMileageItem(ctx, current, s.gameplayNow()); err == nil {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	fresh = newGroup(peerDB)
	registry, err := dnfguild.Load(ctx, fresh.Settings)
	if err != nil {
		t.Fatal(err)
	}
	guild, member, _ := registry.GuildForCharacter("29")
	if applied.Load() != 4 || guild.Mileage != 400 || member.ContributionPoints != 0 || member.Mileage != 0 || guild.Experience != 0 || len(guild.MileageHistory) != 4 {
		t.Fatalf("commits=%d mileage=%d", applied.Load(), guild.Mileage)
	}
	events := map[string]int{}
	for _, event := range guild.MileageHistory {
		if event.Kind != 8 || event.Amount != 100 || event.CharacterName == "" || event.At.IsZero() {
			t.Fatalf("persisted history=%+v", event)
		}
		events[event.CharacterID]++
	}
	if events["29"] != 2 || events["30"] != 2 {
		t.Fatalf("persistent history counts=%v", events)
	}
	for _, id := range []string{"29", "30"} {
		inv, _, err := fresh.Inventory.Load(ctx, id)
		if err != nil || inv.Slots["0:87"].Count != 0 {
			t.Fatalf("remaining inventory id=%s err=%v", id, err)
		}
	}
}
