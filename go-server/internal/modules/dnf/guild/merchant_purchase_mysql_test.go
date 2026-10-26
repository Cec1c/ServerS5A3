package guild

import (
	"context"
	"database/sql"
	"errors"
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

// This test creates and drops only a unique disposable database. Without an
// explicitly isolated DSN it skips rather than touching a configured server.
func TestMerchantPurchaseMySQLAtomicRollbackConcurrencyAndReconnect(t *testing.T) {
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
	name := fmt.Sprintf("merchant_purchase_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{
		AutoCreate: true, CreateDatabases: true, DatabasePlan: plan,
	}); err != nil {
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
	if err = repos.Character.Save(ctx, r.CharacterRecord{
		CharacterID: "1", AccountID: "a1", Name: "会长", Level: 90, Job: "0",
		Stats: map[string]int64{"gold": 900000, "grow_type": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Inventory.Save(ctx, r.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]r.ItemStack{"0:1": {ItemID: 777, Count: 1}},
		Warehouse:   map[string]r.ItemStack{"0:0": {ItemID: 888, Count: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	reg, g, _, _ := textRegistry(t)
	g.Essence = EssenceBalance{AP: 1000, GP: 20000}
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err = Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	catalog := merchantPurchaseCatalogFixture()
	base := MerchantPurchaseRequest{
		Owner: r.GuildOwner{CharacterID: "1", AccountID: "a1"}, GuildID: 1,
		RequestID: fmt.Sprintf("%064x", 1), ItemID: merchantPurchaseDefenceItem,
		Quantity: 2, ShopID: 201, NPCID: 421,
		Revalidate:  func() bool { return true },
		CanInteract: func(r.CharacterRecord, *Guild, bool) bool { return true },
	}

	first, err := ApplyMerchantPurchase(ctx, repos.Guild, base, catalog, merchantPurchaseSuccessfulGrant, now)
	if err != nil || first.CostAP != 4 || first.BalanceAfter != 996 {
		t.Fatalf("defence purchase=%+v err=%v", first, err)
	}
	mercenary := base
	mercenary.RequestID = fmt.Sprintf("%064x", 2)
	mercenary.ItemID, mercenary.Quantity = merchantPurchaseMercenary, 1
	mercenary.ShopID, mercenary.NPCID = 211, 411
	second, err := ApplyMerchantPurchase(ctx, repos.Guild, mercenary, catalog, merchantPurchaseSuccessfulGrant, now.Add(time.Second))
	if err != nil || second.CostAP != 7 || second.BalanceAfter != 989 {
		t.Fatalf("mercenary purchase=%+v err=%v", second, err)
	}
	fresh := newGroup()
	inventory, found, err := fresh.Inventory.Load(ctx, "1")
	if err != nil || !found || inventory.Slots["0:50"].Count != 2 || inventory.Slots["0:51"].Count != 1 ||
		inventory.Slots["0:1"].Count != 1 || inventory.Warehouse["0:0"].Count != 2 {
		t.Fatalf("reconnected inventory=%+v err=%v", inventory, err)
	}
	loaded, err := Load(ctx, fresh.Settings)
	if err != nil || loaded.Guilds[1].Essence.AP != 989 ||
		len(loaded.MerchantPurchaseReceipts) != 2 {
		t.Fatalf("reconnected registry=%+v err=%v", loaded, err)
	}

	t.Run("registry_save_failure_rolls_back_inventory_and_ap", func(t *testing.T) {
		request := base
		request.RequestID = fmt.Sprintf("%064x", 3)
		beforeInventory, _, _ := newGroup().Inventory.Load(ctx, "1")
		beforeRegistry, _, _ := newGroup().Settings.Load(ctx, RegistryScope)
		beforeCharacter, _, _ := newGroup().Character.Load(ctx, "1")
		trigger := "CREATE TRIGGER `" + name + "`.reject_merchant_purchase BEFORE INSERT ON `" + name + "`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test merchant purchase save failure'"
		if _, err := db.ExecContext(ctx, trigger); err != nil {
			t.Fatal(err)
		}
		got, err := ApplyMerchantPurchase(ctx, newGroup().Guild, request, catalog, merchantPurchaseSuccessfulGrant, now.Add(2*time.Second))
		if _, dropErr := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_merchant_purchase"); dropErr != nil {
			t.Fatal(dropErr)
		}
		if err == nil || got.GuildID != 0 {
			t.Fatalf("accepted failed SQL commit: %+v %v", got, err)
		}
		afterInventory, _, _ := newGroup().Inventory.Load(ctx, "1")
		afterRegistry, _, _ := newGroup().Settings.Load(ctx, RegistryScope)
		afterCharacter, _, _ := newGroup().Character.Load(ctx, "1")
		if !reflect.DeepEqual(beforeInventory, afterInventory) ||
			!reflect.DeepEqual(beforeRegistry, afterRegistry) ||
			!reflect.DeepEqual(beforeCharacter, afterCharacter) {
			t.Fatal("late registry failure left inventory, actor or AP changed")
		}
	})

	t.Run("concurrent_last_balance", func(t *testing.T) {
		reset := newGroup()
		state, err := Load(ctx, reset.Settings)
		if err != nil {
			t.Fatal(err)
		}
		state.Guilds[1].Essence.AP = 5
		if err := Save(ctx, reset.Settings, state, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		groups := []r.Group{newGroup(), newGroup()}
		var wg sync.WaitGroup
		results := make(chan error, 8)
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				request := base
				request.RequestID = fmt.Sprintf("%064x", i+10)
				request.Quantity = 1
				_, err := ApplyMerchantPurchase(ctx, groups[i%2].Guild, request, catalog, merchantPurchaseSuccessfulGrant, now.Add(4*time.Second))
				results <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			} else if !errors.Is(err, ErrMerchantAP) {
				t.Fatalf("concurrent error=%v", err)
			}
		}
		state, err = Load(ctx, newGroup().Settings)
		if err != nil || success != 2 || state.Guilds[1].Essence.AP != 1 ||
			len(state.MerchantPurchaseReceipts) != 4 {
			t.Fatalf("success=%d registry=%+v err=%v", success, state, err)
		}
		inventory, _, err := newGroup().Inventory.Load(ctx, "1")
		if err != nil || inventory.Slots["0:50"].Count != 4 || inventory.Slots["0:51"].Count != 1 {
			t.Fatalf("concurrent inventory=%+v err=%v", inventory, err)
		}

		for requestID, receipt := range state.MerchantPurchaseReceipts {
			request := base
			request.RequestID = requestID[len(merchantPurchaseKeyPrefix):]
			request.ItemID = receipt.ItemID
			request.Quantity = receipt.Quantity
			request.ShopID = receipt.ShopID
			request.NPCID = receipt.NPCID
			replayed, err := ApplyMerchantPurchase(ctx, newGroup().Guild, request, catalog, merchantPurchaseSuccessfulGrant, now.Add(24*time.Hour))
			if err != nil || replayed.CostAP != receipt.CostAP ||
				!replayed.ExpireAt.Equal(receipt.ExpireAt) ||
				!replayed.At.Equal(receipt.At) {
				t.Fatalf("reconnect receipt=%+v want=%+v err=%v", replayed, receipt, err)
			}
		}
	})
}
