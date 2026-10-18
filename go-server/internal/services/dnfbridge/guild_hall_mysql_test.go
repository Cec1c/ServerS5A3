package dnfbridge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// Opt in only on a disposable server. The test owns one unique database and
// verifies membership reload, actual login writes and late SQL rollback.
func TestGuildHallMySQLLoginMembershipAndRollback(t *testing.T) {
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
	ctx := context.Background()
	name := fmt.Sprintf("guild_hall_test_%d", time.Now().UnixNano())
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	newGroup := func() dnfrepo.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	s, peers, seed := guildHallFixture(t)
	group := newGroup()
	for _, id := range []string{"29", "32"} {
		c, _, _ := seed.Character.Load(ctx, id)
		c.Stats["gold"], c.Stats["hall_test_marker"] = 867530, 42
		origin := currentTownPositionSnapshot{CharacterID: uint16(currentGuildNumericID(id)), TownID: 8, AreaID: 0, PositionX: 500, PositionY: 220, MovementCode: 5, PositionValid: true}
		currentTownPrevVillageApplyOriginStats(c.Stats, origin)
		if err := group.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := dnfguild.Load(ctx, seed.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(ctx, group.Settings, registry, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	// Fresh repositories must read the durable membership, not the fixture.
	group = newGroup()
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	t.Setenv("DNFBRIDGE_LOGIN_PERSISTED_TOWN", "keep")
	member, _, _ := group.Character.Load(ctx, "29")
	_, town, area, _, _, err := s.currentCharacterListLoginTransition(ctx, peers[29], 29, member)
	if err != nil || town != 8 || area != 0 {
		t.Fatalf("member login=%d/%d err=%v", town, area, err)
	}
	before, _, _ := group.Character.Load(ctx, "32")
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.`reject_hall_stats` BEFORE INSERT ON `"+name+"`.`dnf_character_stats` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected hall login failure'"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := s.currentCharacterListLoginTransition(ctx, peers[32], 32, before); err == nil {
		t.Fatal("late SQL failure committed hall redirect")
	}
	reader := newGroup()
	afterFailure, _, _ := reader.Character.Load(ctx, "32")
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatal("failed redirect changed durable location or unrelated assets")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.`reject_hall_stats`"); err != nil {
		t.Fatal(err)
	}
	_, town, area, _, _, err = s.currentCharacterListLoginTransition(ctx, peers[32], 32, afterFailure)
	if err != nil || town != 38 || area != 1 {
		t.Fatalf("former member login=%d/%d err=%v", town, area, err)
	}
	stored, _, _ := reader.Character.Load(ctx, "32")
	if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 || stored.Stats[currentTownPrevVillageTownStat] == 8 || stored.Stats["gold"] != 867530 || stored.Stats["hall_test_marker"] != 42 {
		t.Fatalf("durable redirect=%v", stored.Stats)
	}
}
