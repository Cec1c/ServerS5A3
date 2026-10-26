package guild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestGuildPlacementMySQLRollbackConcurrencyAndReconnect(t *testing.T) {
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
	name := fmt.Sprintf("guild_placement_test_%d", time.Now().UnixNano())
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
	repos := newGroup()
	if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a1", Name: "会长", Level: 90, Stats: map[string]int64{"gold": 900000}}); err != nil {
		t.Fatal(err)
	}

	seed := func() (PlacementRequest, time.Time) {
		memory, q, now := placementFixture(t)
		inv, _, err := memory.Inventory.Load(ctx, "1")
		if err != nil {
			t.Fatal(err)
		}
		if err = repos.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
		reg, err := Load(ctx, memory.Settings)
		if err != nil {
			t.Fatal(err)
		}
		if err = Save(ctx, repos.Settings, reg, now); err != nil {
			t.Fatal(err)
		}
		return q, now
	}
	for _, mode := range []string{"settings_save", "after_save"} {
		t.Run(mode, func(t *testing.T) {
			q, now := seed()
			before, _, _ := repos.Inventory.Load(ctx, "1")
			regBefore, _ := Load(ctx, repos.Settings)
			if mode == "settings_save" {
				_, err = db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_placement BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test placement save failure'")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			got, err := ApplyPlacement(ctx, repos.Guild, q, now)
			if mode == "settings_save" {
				if _, dropErr := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_placement"); dropErr != nil {
					t.Fatal(dropErr)
				}
			}
			if err == nil || got.GuildID != 0 {
				t.Fatalf("unexpected commit: %+v %v", got, err)
			}
			after, _, err := newGroup().Inventory.Load(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			regAfter, err := Load(ctx, newGroup().Settings)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(regBefore, regAfter) {
				t.Fatal("transaction did not roll back", err)
			}
		})
	}
	t.Run("move-remove-restart-and-rollback", func(t *testing.T) {
		create, now := seed()
		p, err := ApplyPlacement(ctx, repos.Guild, create, now)
		if err != nil {
			t.Fatal(err)
		}
		q := PlacementChangeRequest{Owner: create.Owner, GuildID: create.GuildID, RequestID: fmt.Sprintf("%064x", 20), Change: PlacementChange{Action: PlacementMove, ID: p.Placement.ID, ObjectID: p.Placement.ObjectID, Area: p.Placement.Area, X: create.X + 36, Y: create.Y}, Books: map[uint32]PlacementBook{create.Book.ItemID: create.Book}, Map: create.Map, Revalidate: create.Revalidate, CanInteract: create.CanInteract}
		before, _ := Load(ctx, repos.Settings)
		calls := 0
		q.Revalidate = func() bool { calls++; return calls < 3 }
		if _, err = ApplyPlacementChange(ctx, repos.Guild, q, now); err == nil {
			t.Fatal("stale generation committed")
		}
		after, _ := Load(ctx, newGroup().Settings)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("SQL rollback leaked changed cells or receipt")
		}
		q.Revalidate = create.Revalidate
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for range 6 {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := ApplyPlacementChange(ctx, repos.Guild, q, now); errs <- err }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		remove := q
		remove.Change.Action = PlacementRemove
		remove.RequestID = fmt.Sprintf("%064x", 21)
		if _, err = ApplyPlacementChange(ctx, newGroup().Guild, remove, now); err != nil {
			t.Fatal(err)
		}
		for _, request := range []PlacementChangeRequest{q, remove} {
			if _, err = ApplyPlacementChange(ctx, newGroup().Guild, request, now); err != nil {
				t.Fatal(err)
			}
		}
		after, err = Load(ctx, newGroup().Settings)
		if err != nil || len(after.Guilds[1].Placements.Rows) != 0 || len(after.PlacementChangeReceipts) != 2 {
			t.Fatal("SQL replay resurrected object", err)
		}
		inv, _, err := newGroup().Inventory.Load(ctx, "1")
		if err != nil || inv.Slots["0:42"].Count != 2 {
			t.Fatal("move/remove changed book debit", err)
		}
	})
	for _, sameRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(sameRequest), func(t *testing.T) {
			q, now := seed()
			var wg sync.WaitGroup
			results := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					request := q
					if !sameRequest {
						request.RequestID = fmt.Sprintf("%064x", i+1)
					}
					_, err := ApplyPlacement(ctx, repos.Guild, request, now)
					results <- err
				}(i)
			}
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				}
			}
			expected := 1
			if sameRequest {
				expected = 12
			}
			if successes != expected {
				t.Fatalf("commits %d want %d", successes, expected)
			}
			restarted := newGroup()
			reg, err := Load(ctx, restarted.Settings)
			if err != nil || len(reg.Guilds[1].Placements.Rows) != 1 || len(reg.PlacementReceipts) != 1 {
				t.Fatal("persisted placement", reg, err)
			}
			inv, _, err := restarted.Inventory.Load(ctx, "1")
			if err != nil || inv.Slots["0:42"].Count != 2 {
				t.Fatal("debit", err)
			}
			for key, receipt := range reg.PlacementReceipts {
				q.RequestID = key[len(placementReceiptPrefix):]
				got, err := ApplyPlacement(ctx, restarted.Guild, q, now.Add(2*time.Hour))
				if err != nil || !reflect.DeepEqual(got, receipt) {
					t.Fatal("restart replay", got, err)
				}
			}
			invAfter, _, _ := restarted.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(inv, invAfter) {
				t.Fatal("replay debited book")
			}
		})
	}
}
