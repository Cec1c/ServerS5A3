package epicbook

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	mysqlrepo "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestEpicBookMySQLAtomicCraftAndPickupRound9(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, e := driver.ParseDSN(dsn)
	if e != nil {
		t.Fatal("invalid disposable test DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal("test database open failed")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("epicbook_test_%d", time.Now().UnixNano())
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if e = mysqlrepo.EnsureMySQLSchema(ctx, db, mysqlrepo.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); e != nil {
			t.Error(e)
		}
	}()
	g, e := mysqlrepo.NewMySQLGroupFromDB(db, mysqlrepo.MySQLGroupOptions{DatabasePlan: plan})
	if e != nil {
		t.Fatal(e)
	}
	seed := func() {
		for _, e := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"epic_piece:10300000": "1000", "other": "keep"}}), g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:364": {ItemID: 10099775, Count: 100}, "0:365": {ItemID: 10158124, Count: 100}}})} {
			if e != nil {
				t.Fatal(e)
			}
		}
		for slot, id := range []string{"19", "20"} {
			if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "a", Slot: slot, Name: "epic" + id, Level: 90}); e != nil {
				t.Fatal(e)
			}
			if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); e != nil {
				t.Fatal(e)
			}
		}
	}
	c, e := LoadCatalog(textSource(twoRecipes))
	if e != nil {
		t.Fatal(e)
	}
	project := func(_ context.Context, _ repo.CharacterRecord, i *repo.InventoryRecord, id uint32) error {
		if i.Slots == nil {
			i.Slots = map[string]repo.ItemStack{}
		}
		i.Slots["0:9"] = repo.ItemStack{ItemID: int64(id), Count: 1}
		return nil
	}
	assets := g.AccountAssets.(repo.EpicBookAssetUnitOfWork)
	seed()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, id := range []string{"19", "20"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, e := Craft(ctx, assets, c, "a", id, 450004, time.Now(), project); e == nil {
				successes.Add(1)
			}
		}(id)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("shared cost granted twice", successes.Load())
	}
	a, _, e := g.Account.Load(ctx, "a")
	if e != nil || a.Metadata["epic_piece:10300000"] != "0" || a.Metadata["other"] != "keep" {
		t.Fatal("debit mismatch", a, e)
	}
	seed()
	beforeA, _, _ := g.Account.Load(ctx, "a")
	beforeS, _, _ := g.AccountInventory.Load(ctx, "a")
	beforeI, _, _ := g.Inventory.Load(ctx, "19")
	if _, e = db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.`reject_epic_grant` BEFORE INSERT ON `"+name+"`.`dnf_inventory_items` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'epic book grant abort'"); e != nil {
		t.Fatal(e)
	}
	if _, e = Craft(ctx, assets, c, "a", "19", 450004, time.Now(), project); e == nil {
		t.Fatal("late failure committed")
	}
	a, _, _ = g.Account.Load(ctx, "a")
	s, _, _ := g.AccountInventory.Load(ctx, "a")
	i, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(s, beforeS) || !reflect.DeepEqual(i, beforeI) {
		t.Fatal("SQL did not roll back all three asset owners")
	}
	if _, e = db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.`reject_epic_grant`"); e != nil {
		t.Fatal(e)
	}
	seed()
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, e := Craft(ctx, assets, c, "a", "19", 450004, time.Now(), project); e != nil {
			t.Error(e)
		}
	}()
	go func() {
		defer wg.Done()
		if _, e := repo.AddAccountEpicPieceBalance(ctx, g.Account, "a", 10300000, 7, time.Now()); e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	a, _, _ = g.Account.Load(ctx, "a")
	n, _ := strconv.Atoi(a.Metadata["epic_piece:10300000"])
	if n != 7 {
		t.Fatal("concurrent credit lost", n)
	}
}
