//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/packageitem"
	"longheng.io/server/internal/modules/dnf/repository"
)

type fullLedgerMailboxComparison struct {
	repository.AccountCharacterAssetUnitOfWork
	ledger repository.AccountCharacterLedgerMailboxAssetUnitOfWork
}

func (u fullLedgerMailboxComparison) WithinAccountCharacterLedgerMailboxAssets(ctx context.Context, a, c string, apply func(repository.AccountRepository, repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error) error {
	return u.ledger.WithinAccountCharacterLedgerMailboxAssets(ctx, a, c, func(ar repository.AccountRepository, ai repository.AccountInventoryRepository, cr repository.CharacterRepository, ir repository.InventoryRepository, er repository.EquipmentRepository, mr repository.MailboxRepository) error {
		ar.(*rentalScopedAccountRepository).base.(*mysqlAccountStore).trackMetadataChanges = false
		cr.(*rentalScopedCharacterRepository).base.(*mysqlCharStore).trackStatChanges = false
		ir.(*rentalScopedInventoryRepository).base.(*mysqlBagStore).trackSlotChanges = false
		return apply(ar, ai, cr, ir, er, mr)
	})
}

func TestSeriaLedgerMailboxDeltaDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires loopback DSN without existing schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires isolated loopback")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, batch := range []string{"1", "200"} {
		t.Run("batch_"+batch, func(t *testing.T) {
			t.Setenv(currentMysqlInsertBatchEnv, batch)
			var accounts []repository.AccountRecord
			var characters []repository.CharacterRecord
			var inventories []repository.InventoryRecord
			var results []packageitem.MagicBoxResult
			var writes []int
			for _, full := range []bool{true, false} {
				name := fmt.Sprintf("seria_delta_%d", time.Now().UnixNano())
				defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
				plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
				if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
					t.Fatal(err)
				}
				g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
				if err != nil {
					t.Fatal(err)
				}
				at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				a := repository.AccountRecord{AccountID: "acct-77", CreatedAt: at, UpdatedAt: at, Metadata: map[string]string{packageitem.SeriaLuckMetadataKey: "7"}}
				c := repository.CharacterRecord{CharacterID: "77", AccountID: a.AccountID, Slot: 0, Name: "fixture", Level: 90, CreatedAt: at, UpdatedAt: at, Stats: map[string]int64{}}
				i := repository.InventoryRecord{CharacterID: "77", UpdatedAt: at, Slots: map[string]repository.ItemStack{}, Warehouse: map[string]repository.ItemStack{}}
				for n := 0; n < 160; n++ {
					a.Metadata[fmt.Sprintf("keep_account_%03d", n)] = "unchanged"
				}
				for n := 0; n < 60; n++ {
					c.Stats[fmt.Sprintf("keep_character_%03d", n)] = int64(n)
					i.Slots[fmt.Sprintf("0:%d", n)] = bagMoveStackable(int64(500000+n), 5)
				}
				i.Slots["0:67"] = bagMoveStackable(2682272, 20)
				i.Slots["0:70"] = repository.ItemStack{ItemID: 2600014, Count: 5, Extra: map[string]string{"item_kind": "stackable"}}
				for _, save := range []func() error{func() error { return g.Account.Save(ctx, a) }, func() error { return g.Character.Save(ctx, c) }, func() error { return g.Inventory.Save(ctx, i) }} {
					if err := save(); err != nil {
						t.Fatal(err)
					}
				}
				if full {
					g.AccountAssets = fullLedgerMailboxComparison{g.AccountAssets, g.AccountAssets.(repository.AccountCharacterLedgerMailboxAssetUnitOfWork)}
				}
				owner, err := packageitem.NewOwner(g)
				if err != nil {
					t.Fatal(err)
				}
				readWrites := func() int {
					n := 0
					for _, variable := range []string{"Com_insert", "Com_delete", "Com_update"} {
						var key string
						var count int
						if err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE '"+variable+"'").Scan(&key, &count); err != nil {
							t.Fatal(err)
						}
						n += count
					}
					return n
				}
				before := readWrites()
				result, err := owner.ApplyMagicBoxExpand(ctx, packageitem.MagicBoxExpandCommand{SelectedCharacterID: 77, AccountID: a.AccountID, ListType: 0, SlotIndex: 67, BoxItemID: 2682272, MaterialSlotIndex: -1, OpenCount: 10}, func(int64) (alignedcmd.MagicBoxResolution, error) {
					return alignedcmd.MagicBoxResolution{Kind: "random", Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 2600014, Weight: 100, Count: 2}}}}, OnOpen: func(ch *repository.CharacterRecord) (bool, error) {
						ch.Stats["private_activity_counter"]++
						return true, nil
					}}, nil
				}, func(id int64) (alignedcmd.MagicBoxRewardItem, error) {
					return alignedcmd.MagicBoxRewardItem{ItemID: id, Kind: "stackable", SlotStart: 65, SlotEnd: 120}, nil
				})
				writes = append(writes, readWrites()-before)
				if err != nil || !result.Success || result.SeriaLuckBefore != 7 || result.SeriaLuckAfter != 1 || !result.SeriaLuckDoubleTriggered {
					t.Fatalf("real Seria open: result=%+v err=%v", result, err)
				}
				a, _, err = g.Account.Load(ctx, a.AccountID)
				if err != nil {
					t.Fatal(err)
				}
				c, _, err = g.Character.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				i, _, err = g.Inventory.Load(ctx, "77")
				if err != nil {
					t.Fatal(err)
				}
				rewardCount := int64(0)
				for _, stack := range i.Slots {
					if stack.ItemID == 2600014 {
						rewardCount += stack.Count
					}
				}
				if a.Metadata[packageitem.SeriaLuckMetadataKey] != "1" || c.Stats["private_activity_counter"] != 1 || i.Slots["0:67"].Count != 10 || rewardCount != 29 {
					t.Fatalf("full=%t persisted luck=%s activity=%d boxes=%d rewards=%d overflow=%s", full, a.Metadata[packageitem.SeriaLuckMetadataKey], c.Stats["private_activity_counter"], i.Slots["0:67"].Count, rewardCount, result.OverflowMailID)
				}
				a.UpdatedAt, c.UpdatedAt, i.UpdatedAt = at, at, at
				accounts = append(accounts, a)
				characters = append(characters, c)
				inventories = append(inventories, i)
				results = append(results, result)
			}
			if !reflect.DeepEqual(accounts[0], accounts[1]) || !reflect.DeepEqual(characters[0], characters[1]) || !reflect.DeepEqual(inventories[0], inventories[1]) || !reflect.DeepEqual(results[0], results[1]) {
				t.Fatal("real MariaDB full/delta Seria results or persisted assets differ")
			}
			if writes[1] >= writes[0] {
				t.Fatalf("no real SQL reduction: %v", writes)
			}
			t.Logf("MariaDB Seria ten-open SQL writes %d -> %d; results and all assets equal", writes[0], writes[1])
		})
	}
}
