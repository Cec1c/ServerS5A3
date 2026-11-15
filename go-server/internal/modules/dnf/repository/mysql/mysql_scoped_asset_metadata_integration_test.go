//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

func TestScopedAssetMetadataDeltaDisposableMySQL(t *testing.T) {
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
	for _, batch := range []string{"1", "200"} {
		for _, mode := range []string{"ledger", "plain", "pet"} {
			t.Run("batch_"+batch+"/"+mode, func(t *testing.T) {
				t.Setenv(currentMysqlInsertBatchEnv, batch)
				var accounts []repository.AccountRecord
				var characters []repository.CharacterRecord
				var writes []int
				for _, full := range []bool{true, false} {
					name := fmt.Sprintf("scoped_metadata_%d", time.Now().UnixNano())
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
					a := repository.AccountRecord{AccountID: "acct-77", HonorExp: 100, CreatedAt: at, UpdatedAt: at, Metadata: map[string]string{"adventure_group_runtime": "initial"}}
					c := repository.CharacterRecord{CharacterID: "77", AccountID: a.AccountID, Slot: 0, Name: "fixture", Level: 90, CreatedAt: at, UpdatedAt: at, Stats: map[string]int64{"exp": 100}}
					for n := 0; n < 160; n++ {
						a.Metadata[fmt.Sprintf("账号无关项%d", n)] = "unchanged"
					}
					for n := 0; n < 80; n++ {
						c.Stats[fmt.Sprintf("角色无关项%d", n)] = int64(n)
					}
					if err := g.Account.Save(ctx, a); err != nil {
						t.Fatal(err)
					}
					if err := g.Character.Save(ctx, c); err != nil {
						t.Fatal(err)
					}
					u := g.AccountAssets.(*mysqlAccountCharacterAssetUnitOfWork)
					before := readWrites()
					if err := applyScopedMetadataAssets(ctx, u, mode, full, false); err != nil {
						t.Fatal(err)
					}
					writes = append(writes, readWrites()-before)
					a, _, err = g.Account.Load(ctx, a.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					c, _, err = g.Character.Load(ctx, "77")
					if err != nil {
						t.Fatal(err)
					}
					if c.Stats["asset_action_count"] != 1 || c.Stats["exp"] != 103 || mode == "ledger" && (a.HonorExp != 102 || a.Metadata["asset_action_receipt"] != "1") {
						t.Fatal("incorrect durable asset mutation")
					}
					// A callback failure must undo even successful SQL writes.
					injected := errors.New("abort after SQL writes")
					err = withinScopedMetadataAssets(ctx, u, mode, func(ar repository.AccountRepository, cr repository.CharacterRepository) error {
						changed := repository.CloneCharacter(c)
						changed.Stats["asset_action_count"]++
						if err := repository.SaveCharacterFields(ctx, cr, changed, repository.CharacterFieldStats); err != nil {
							return err
						}
						if ar != nil {
							changed := repository.CloneAccount(a)
							changed.HonorExp++
							if err := ar.Save(ctx, changed); err != nil {
								return err
							}
						}
						return injected
					})
					checkA, _, aErr := g.Account.Load(ctx, a.AccountID)
					checkC, _, cErr := g.Character.Load(ctx, "77")
					if !errors.Is(err, injected) || aErr != nil || cErr != nil || !reflect.DeepEqual(a, checkA) || !reflect.DeepEqual(c, checkC) {
						t.Fatal("real SQL callback failure did not roll back", err, aErr, cErr)
					}
					a.UpdatedAt, c.UpdatedAt = at, at
					accounts, characters = append(accounts, a), append(characters, c)
				}
				// With one bulk INSERT, a small character-only update can use the
				// same statement count while binding/writing fewer relation rows.
				allowEqualCount := batch == "200" && mode != "ledger"
				if !reflect.DeepEqual(accounts[0], accounts[1]) || !reflect.DeepEqual(characters[0], characters[1]) || writes[1] > writes[0] || !allowEqualCount && writes[1] == writes[0] {
					t.Fatalf("real SQL full/delta differs or no reduction: writes=%v", writes)
				}
				t.Logf("MariaDB %s SQL writes %d -> %d; durable assets equal and abort rolled back", mode, writes[0], writes[1])
			})
		}
	}
}
