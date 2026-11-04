//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestAssetDeltaDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" {
		t.Fatal("invalid isolated DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("requires loopback without existing schema")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("asset_delta_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	readWrites := func() int {
		var k string
		var inserts, deletes int
		if err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Com_insert'").Scan(&k, &inserts); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Com_delete'").Scan(&k, &deletes); err != nil {
			t.Fatal(err)
		}
		return inserts + deletes
	}
	for _, scenario := range []string{"counter", "delete", "receipt", "rollback", "second_save"} {
		t.Run(scenario, func(t *testing.T) {
			var records []repository.CharacterRecord
			var writes []int
			for _, optimized := range []bool{false, true} {
				// A separate character per branch keeps append-only receipts independent.
				id := fmt.Sprintf("%s_%t", scenario, optimized)
				at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				c := repository.CharacterRecord{CharacterID: id, AccountID: id, Name: id, Level: 89, CreatedAt: at, UpdatedAt: at, Stats: map[string]int64{repository.LevelUpEventKey(80): 1234}}
				for i := 0; i < 80; i++ {
					c.Stats[fmt.Sprintf("keep_%02d", i)] = int64(i)
				}
				if err := g.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				before, found, err := g.Character.Load(ctx, id)
				if err != nil || !found {
					t.Fatalf("seed missing: found=%t err=%v", found, err)
				}
				n := readWrites()
				err = g.CharacterAssets.WithinCharacterAssets(ctx, id, func(chars repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository) error {
					if !optimized {
						chars.(*mysqlCharStore).trackStatChanges = false
					}
					v, found, err := chars.Load(ctx, id)
					if err != nil || !found {
						return fmt.Errorf("locked seed missing: found=%t err=%v", found, err)
					}
					v.Stats["counter"] = 1
					if scenario == "delete" {
						delete(v.Stats, "keep_01")
					}
					if scenario == "receipt" {
						delete(v.Stats, repository.LevelUpEventKey(80))
						v.Stats[repository.LevelUpEventKey(90)] = 2345
					}
					if err := repository.SaveCharacterFields(ctx, chars, v, repository.CharacterFieldStats); err != nil {
						return err
					}
					if scenario == "rollback" {
						return errors.New("isolated late failure")
					}
					if scenario == "second_save" {
						v.Stats["counter"] = 2
						return repository.SaveCharacterFields(ctx, chars, v, repository.CharacterFieldStats)
					}
					return nil
				})
				writes = append(writes, readWrites()-n)
				if (err != nil) != (scenario == "rollback") {
					t.Fatal(err)
				}
				after, _, err := g.Character.Load(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "rollback" && !reflect.DeepEqual(before, after) {
					t.Fatal("partial asset transaction survived rollback")
				}
				if scenario == "receipt" && (after.Stats[repository.LevelUpEventKey(80)] != 1234 || after.Stats[repository.LevelUpEventKey(90)] != 2345) {
					t.Fatal("permanent receipts lost")
				}
				after.CharacterID = "compare"
				after.AccountID = "compare"
				after.Name = "compare"
				records = append(records, after)
			}
			if !reflect.DeepEqual(records[0], records[1]) {
				t.Fatal("real database full and delta states differ")
			}
			if scenario == "counter" && (writes[0] < 80 || writes[1] != 2) {
				t.Fatalf("write amplification remains: %v", writes)
			}
			t.Logf("MariaDB %s INSERT/DELETE %d -> %d; persisted records equal", scenario, writes[0], writes[1])
		})
	}
}
