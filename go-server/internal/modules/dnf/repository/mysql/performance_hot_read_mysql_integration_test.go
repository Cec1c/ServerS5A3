//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPerformanceHotReadDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("explicit disposable database required")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("isolated TCP DSN required")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("loopback required")
	}
	cfg.ParseTime, cfg.InterpolateParams = true, true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := fmt.Sprintf("hot_read_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + schema + "`")
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err = EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, IncludeCSharpLegacySchema: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	obs := &SQLObservation{}
	g, err := NewMySQLGroup(&ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: obs}, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Account.Save(ctx, r.AccountRecord{AccountID: "account", HonorExp: 123456}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		c := r.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: "account", Name: fmt.Sprintf("actor%d", i), Job: "0", Level: 20 + i, Slot: i, Stats: map[string]int64{"wedding_v1_phase": 2, "levelXupXeventX90": 99}}
		for j := 0; j < 200; j++ {
			c.Stats[fmt.Sprintf("unrelated_%d", j)] = int64(j)
		}
		if err = g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err = r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("narrow_reads", func(t *testing.T) {
		before, rowsBefore := optimizationCounts(obs)
		old, err := r.ListAccountCharacterLevels(ctx, optimizationLegacyCharacters{g.Character}, "account", 32)
		if err != nil {
			t.Fatal(err)
		}
		fullCalls, fullRows := optimizationCounts(obs)
		fresh, err := r.ListAccountCharacterLevels(ctx, g.Character, "account", 32)
		if err != nil || !reflect.DeepEqual(old, fresh) {
			t.Fatal("level projection differs", err, old, fresh)
		}
		narrowCalls, narrowRows := optimizationCounts(obs)
		if narrowCalls-fullCalls != 1 || narrowRows-fullRows != 4 {
			t.Fatal("summary still loads supplements")
		}
		t.Logf("four-character summary SQL %d -> %d; rows %d -> %d", fullCalls-before, narrowCalls-fullCalls, fullRows-rowsBefore, narrowRows-fullRows)
		honor, found, err := r.LoadAccountHonorExperience(ctx, g.Account, "account")
		if err != nil || !found || honor != 123456 {
			t.Fatal("honor projection", honor, found, err)
		}
		w, found, err := r.LoadCharacterWedding(ctx, g.Character, "1")
		if err != nil || !found || w.AccountID != "account" || len(w.Stats) != 1 || w.Stats["wedding_v1_phase"] != 2 {
			t.Fatal("wedding projection", w, found, err)
		}
		probe, _, err := r.ProbeLevelUpRewards(ctx, g.Character, "1")
		if err != nil || probe.Pending {
			t.Fatal("underscore wildcard matched unrelated stat", probe, err)
		}
	})
	inv := r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{}}
	for i := 0; i < 250; i++ {
		inv.Slots[fmt.Sprint(i)] = r.ItemStack{ItemID: int64(i + 1), Count: 10, Extra: map[string]string{"keep": "yes"}}
	}
	equip := r.EquipmentRecord{CharacterID: "1", Entries: map[string]r.EquipmentEntry{"weapon": {ItemID: 123, Extra: map[string]string{"durability": "99"}}, "coat": {ItemID: 456}}}
	if err = g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err = g.Equipment.Save(ctx, equip); err != nil {
		t.Fatal(err)
	}
	t.Run("equipment_inventory_commit_and_rollback", func(t *testing.T) {
		for _, rollback := range []bool{true, false} {
			beforeI, _, _ := g.Inventory.Load(ctx, "1")
			beforeE, _, _ := g.Equipment.Load(ctx, "1")
			lateFailure := errors.New("late failure after both saves")
			err := g.CharacterAssets.WithinCharacterAssets(ctx, "1", func(_ r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository) error {
				v, _, err := i.Load(ctx, "1")
				if err != nil {
					return err
				}
				w, _, err := e.Load(ctx, "1")
				if err != nil {
					return err
				}
				x := v.Slots["1"]
				x.Count--
				v.Slots["1"] = x
				y := w.Entries["weapon"]
				y.Extra["durability"] = "98"
				w.Entries["weapon"] = y
				if err := i.Save(ctx, v); err != nil {
					return err
				}
				if err := e.Save(ctx, w); err != nil {
					return err
				}
				if rollback {
					return lateFailure
				}
				return nil
			})
			if (rollback && !errors.Is(err, lateFailure)) || (!rollback && err != nil) {
				t.Fatal(err)
			}
			afterI, _, _ := g.Inventory.Load(ctx, "1")
			afterE, _, _ := g.Equipment.Load(ctx, "1")
			if rollback {
				if !reflect.DeepEqual(beforeI, afterI) || !reflect.DeepEqual(beforeE, afterE) {
					t.Fatal("partial save escaped rollback")
				}
			} else if afterI.Slots["1"].Count != 9 || len(afterI.Slots) != 250 || afterE.Entries["weapon"].Extra["durability"] != "98" || !reflect.DeepEqual(beforeE.Entries["coat"], afterE.Entries["coat"]) {
				t.Fatal("delta damaged unrelated state")
			}
		}
	})
	t.Run("buff_snapshot_does_not_block_writer_and_stays_consistent", func(t *testing.T) {
		u := g.CeraShopAssets.(r.BuffOwnedSnapshotUnitOfWork)
		err := u.WithinBuffOwnedSnapshot(ctx, "account", "1", "character:1:buff_swap_native", func(_ r.CharacterRepository, i r.InventoryRepository, e r.EquipmentRepository, _ r.SettingsRepository, _ r.BuffPetMetadataReader, _ r.OwnedLegacyInventoryRepository) error {
			before, _, err := e.Load(ctx, "1")
			if err != nil {
				return err
			}
			if _, _, err := i.Load(ctx, "1"); err != nil {
				return err
			}
			writerCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			_, err = db.ExecContext(writerCtx, "UPDATE `"+schema+"`.`dnf_equipment_entry_extra` SET extra_value='97' WHERE character_id='1' AND extra_key='durability'")
			if err != nil {
				return fmt.Errorf("read snapshot blocked writer: %w", err)
			}
			after, _, err := e.Load(ctx, "1")
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(before, after) {
				return errors.New("read view changed after concurrent commit")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		fresh, _, err := g.Equipment.Load(ctx, "1")
		if err != nil || fresh.Entries["weapon"].Extra["durability"] != "97" {
			t.Fatal("writer did not commit", err)
		}
	})
	t.Run("notifications_only_after_commit", func(t *testing.T) {
		wake := make(chan string, 8)
		stop := g.LevelUpChanges.Subscribe(func(id string) { wake <- id })
		defer stop()
		for _, rollback := range []bool{true, false} {
			fail := errors.New("rollback")
			err := g.CharacterAssets.WithinCharacterAssets(ctx, "1", func(chars r.CharacterRepository, _ r.InventoryRepository, _ r.EquipmentRepository) error {
				v, _, err := chars.Load(ctx, "1")
				if err != nil {
					return err
				}
				v.Level = 90
				v.Stats[r.LevelUpEventKey(90)] = 123
				if err := r.SaveCharacterFields(ctx, chars, v, r.CharacterFieldBase, r.CharacterFieldStats); err != nil {
					return err
				}
				if len(wake) != 0 {
					return errors.New("uncommitted notification")
				}
				if rollback {
					return fail
				}
				return nil
			})
			if rollback {
				if !errors.Is(err, fail) || len(wake) != 0 {
					t.Fatal("rollback notified", err)
				}
			} else {
				if err != nil || len(wake) != 1 || <-wake != "1" {
					t.Fatal("commit lost notification", err)
				}
				probe, _, err := r.ProbeLevelUpRewards(ctx, g.Character, "1")
				if err != nil || !probe.Pending {
					t.Fatal("notification preceded durable event", err)
				}
			}
		}
	})
}
