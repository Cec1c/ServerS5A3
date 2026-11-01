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
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFRecommendedClearMySQLSettlement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	ctx := context.Background()
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	w, e := worldmap.LoadSource(ctx, a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	p, e := progression.Load(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	m, e := progression.LoadMonsterExperienceSources(a)
	if e != nil {
		t.Fatal(e)
	}
	c, e := loadCurrentDungeonClearRankCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	h, e := dnfhonor.LoadTables(a)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN") == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	for _, id := range []int{310, 314, 7539} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			s, r, session, conn, _ := prepareCompletedSettlementRuntime(t)
			g := clearSettlementMySQLFixture(t)
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return g, true }
			ch := dnfrepo.CloneCharacter(r.Character)
			ch.Level = 90
			entry, _ := p.ThresholdToNext(89)
			ch.Stats["exp"] = int64(entry)
			for _, err := range []error{
				g.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: ch.AccountID, Metadata: map[string]string{}}),
				g.Character.Save(ctx, ch),
				g.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: ch.CharacterID, Points: dnfrepo.SkillPointState{SyncedLevel: 90, TotalSP: 11970, RemainingSP: 270, TotalTP: 41, RemainingTP: 7}}),
				g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]dnfrepo.ItemStack{}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			r.Character = ch
			s.honorTable = h
			r.entryCharacterLevel = 90
			r.ordinaryFinalRoomClearAccepted = true
			s.initialEquipmentArchive = a
			s.initialEquipmentLoadErr = nil
			s.dungeonMonsterTable, e = newPVFDungeonMonsterCatalog(a)
			if e != nil {
				t.Fatal(e)
			}
			d, ok := w.FindDungeon(int64(id))
			if !ok {
				t.Fatal("map missing")
			}
			r.Dungeon = d
			r.Request = dungeoncmd.SelectDungeonRequest{DungeonID: uint32(id), Difficulty: 0}
			r.settlementPlayResultReceived = true
			r.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			r.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 99
			e := s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, r, currentDungeonSettlementRewardResources{Progression: p, ExperienceSources: m, ClearRankCatalog: c})
			if e != nil {
				t.Fatal(e)
			}
			if r.settlementResultPlan == nil || !currentDungeonIsRecommendedClear(r) {
				t.Fatal("recommended clear did not produce a result")
			}
			before, found, e := g.Character.Load(ctx, ch.CharacterID)
			if e != nil || !found {
				t.Fatal("settled character missing", e)
			}
			conn.write.Reset()
			if e := s.handleDungeonSetPlayResult(session, r.settlementPlayResultBody); e != nil {
				t.Fatal(e)
			}
			stream := conn.write.Bytes()
			// Actual card resources include the frozen contract state before op35.
			for _, want := range []uint16{34, 37, 903, 35} {
				packet, rest := splitGameServerUpperPacket(t, stream)
				class := byte(0)
				if want == 903 {
					class = 1
				}
				if packet.Header.MsgID != want || packet.Header.Classification != class {
					t.Fatalf("result packet=%+v want=%d", packet.Header, want)
				}
				stream = rest
			}
			if !r.settlementClearRewardSent {
				t.Fatal("clear rewards not sent")
			}
			written := conn.write.Len()
			if e := s.handleDungeonSetPlayResult(session, r.settlementPlayResultBody); e != nil || conn.write.Len() != written {
				t.Fatal("replay resent result", e)
			}
			after, _, e := g.Character.Load(ctx, ch.CharacterID)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("replay changed committed character", e)
			}
			events, e := g.Activity.Events(ctx, dnfrepo.ActivityQuery{AccountID: ch.AccountID, CharacterID: ch.CharacterID})
			if e != nil {
				t.Fatal(e)
			}
			recommended := 0
			for _, event := range events {
				if event.Metric == dnfrepo.ActivityRecommendedDungeon {
					recommended++
				}
			}
			if recommended != 1 {
				t.Fatalf("recommended clear event count=%d", recommended)
			}
			conn.write.Reset()
			if e := s.handleDungeonScoreScrollState(session, nil); e != nil {
				t.Fatal(e)
			}
			if !r.settlementCardScrollStateSent {
				t.Fatal("score scroll ACK missing")
			}
			conn.write.Reset()
			if e := s.handleDungeonCardSelectRightState(session, nil); e != nil {
				t.Fatal(e)
			}
			if !r.settlementCardLayoutSent {
				t.Fatal("card layout missing")
			}

		})
	}
}

func clearSettlementMySQLFixture(t *testing.T) dnfrepo.Group {
	t.Helper()
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable mysql required")
	}
	cfg, e := driver.ParseDSN(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	name := fmt.Sprintf("clear_settlement_test_%d", time.Now().UnixNano())
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if e := repomysql.EnsureMySQLSchema(context.Background(), db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); e != nil {
		db.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := db.Exec("DROP DATABASE `" + name + "`"); e != nil {
			t.Error(e)
		}
		db.Close()
	})
	g, e := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
