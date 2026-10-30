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

func TestRuntimeDeltasDisposableMySQL(t *testing.T) {
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
	name := fmt.Sprintf("runtime_delta_%d", time.Now().UnixNano())
	defer func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			t.Error(err)
		}
	}()
	plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	var adapter SQLDB = SQLDBAdapter{DB: db}
	if os.Getenv("DNF_RUNTIME_PREPARED") == "1" {
		prepared := NewPreparedSQLDB(db, 64)
		defer prepared.Close()
		adapter = prepared
	}
	g, err := NewMySQLGroup(adapter, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("progression-authority-and-rollback", func(t *testing.T) {
		runProgressionRealDBCase(t, ctx, db, name, g)
	})
	t.Run("active-quest-dependency-view", func(t *testing.T) { runActiveQuestRealDBCase(t, ctx, db, name) })
	t.Run("account-metadata-mailbox", func(t *testing.T) {
		runAccountMetadataRealDBCase(t, ctx, db, name, g)
	})
	t.Run("quest-history-conflict-rollback", func(t *testing.T) {
		original := repository.QuestRecord{CharacterID: "q", States: map[int64]repository.QuestState{}, UpdatedAt: time.Now().UTC().Truncate(time.Second)}
		for id := int64(1); id <= 674; id++ {
			original.States[id] = repository.QuestState{Status: "completed", Extra: map[string]string{"receipt": "keep", "reward_state": "granted"}}
		}
		original.States[1] = repository.QuestState{Status: "active", ProgressValue: 2, Extra: map[string]string{"counter": "2", "receipt": "live"}}
		if err := g.Quest.Save(ctx, original); err != nil {
			t.Fatal(err)
		}
		before, _, err := repository.LoadQuestPlanning(ctx, g.Quest, "q")
		if err != nil {
			t.Fatal(err)
		}
		if !before.PlanningPartial || before.States[674].Extra != nil {
			t.Fatal("history extras fetched")
		}
		after := repository.CloneQuest(before)
		state := after.States[1]
		state.ProgressValue = 1
		state.Extra["counter"] = "1"
		after.States[1] = state
		if err := repository.SaveQuestChanges(ctx, g.Quest, before, after, repository.QuestFieldStates); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveQuestChanges(ctx, g.Quest, before, after, repository.QuestFieldStates); !errors.Is(err, repository.ErrQuestVersionConflict) {
			t.Fatalf("stale=%v", err)
		}
		loaded, _, err := g.Quest.Load(ctx, "q")
		if err != nil {
			t.Fatal(err)
		}
		for id := int64(2); id <= 674; id++ {
			if !reflect.DeepEqual(loaded.States[id].Extra, original.States[id].Extra) {
				t.Fatalf("lost history %d", id)
			}
		}
		if loaded.States[1].ProgressValue != 1 || loaded.States[1].Extra["receipt"] != "live" {
			t.Fatal("bad committed delta")
		}
		// Fail the Extra write after the state UPDATE; the transaction must
		// restore both the state and its original counter.
		trigger := "CREATE TRIGGER `" + name + "`.fail_extra BEFORE INSERT ON `" + name + "`.`dnf_" + mysqlQuestExtraTable + "` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated failure'"
		if _, err := db.ExecContext(ctx, trigger); err != nil {
			t.Fatal(err)
		}
		defer db.Exec("DROP TRIGGER `" + name + "`.fail_extra")
		next := repository.CloneQuest(loaded)
		state = next.States[1]
		state.ProgressValue = 0
		state.Extra["counter"] = "0"
		next.States[1] = state
		if err := repository.SaveQuestChanges(ctx, g.Quest, loaded, next, repository.QuestFieldStates); err == nil {
			t.Fatal("failure not injected")
		}
		check, _, err := g.Quest.Load(ctx, "q")
		if err != nil || check.States[1].ProgressValue != 1 || check.States[1].Extra["counter"] != "1" {
			t.Fatalf("partial write: %+v %v", check.States[1], err)
		}
	})
	t.Run("pet-window-and-rollback", func(t *testing.T) {
		for _, offset := range []int{0, 1, 2} {
			t.Run(fmt.Sprint(offset), func(t *testing.T) {
				entry := repository.PetEntry{PetKey: "p", ItemID: 1, Satiety: 100, Extra: map[string]string{"keep": "yes"}, AppliedClearTokens: map[string]bool{}}
				for i := 0; i < 128; i++ {
					v := fmt.Sprintf("token-%03d", i)
					entry.AppliedClearTokens[v] = true
					entry.AppliedClearTokenOrder = append(entry.AppliedClearTokenOrder, v)
				}
				original := repository.PetRecord{CharacterID: "p", EquippedKey: "p", Entries: map[string]repository.PetEntry{"p": entry, "other": {PetKey: "other", ItemID: 2, Extra: map[string]string{"preserve": "other"}}}, Artifacts: map[string]repository.ItemStack{"red": {ItemID: 3, Count: 1}}}
				if err := g.Pet.Save(ctx, original); err != nil {
					t.Fatal(err)
				}
				if offset > 0 {
					// Test schema-valid legacy layouts: one-based and sparse ordinals.
					factor := 1
					if offset == 2 {
						factor = 2
					}
					if _, err := db.ExecContext(ctx, "UPDATE `"+name+"`.`dnf_"+mysqlPetTokensTable+"` SET token_order=token_order*?+? WHERE character_id='p' ORDER BY token_order DESC", factor, offset); err != nil {
						t.Fatal(err)
					}
				}
				update := func(fail bool) error {
					return g.CharacterPets.WithinCharacterPets(ctx, "p", func(_ repository.InventoryRepository, _ repository.EquipmentRepository, pets repository.PetRepository) error {
						r, _, err := pets.Load(ctx, "p")
						if err != nil {
							return err
						}
						old := repository.ClonePet(r).Entries["p"]
						n := r.Entries["p"]
						n.Satiety = 99
						if !fail {
							delete(n.AppliedClearTokens, "token-000")
							n.AppliedClearTokens["token-128"] = true
							n.AppliedClearTokenOrder = append(n.AppliedClearTokenOrder[1:], "token-128")
						} else {
							n.Satiety = 98
						}
						r.Entries["p"] = n
						r.UpdatedAt = time.Now().UTC()
						if err := repository.SavePetGrowth(ctx, pets, r, "p", old); err != nil {
							return err
						}
						if fail {
							return errors.New("late rollback")
						}
						return nil
					})
				}
				if err := update(false); err != nil {
					t.Fatal(err)
				}
				if err := update(true); err == nil {
					t.Fatal("expected rollback")
				}
				got, _, err := g.Pet.Load(ctx, "p")
				if err != nil {
					t.Fatal(err)
				}
				n := got.Entries["p"]
				if n.Satiety != 99 || len(n.AppliedClearTokens) != 128 || n.AppliedClearTokens["token-000"] || !n.AppliedClearTokens["token-128"] || n.AppliedClearTokenOrder[0] != "token-001" || n.AppliedClearTokenOrder[127] != "token-128" {
					t.Fatalf("bad window/rollback: %+v", n)
				}
				if !reflect.DeepEqual(n.Extra, entry.Extra) || !reflect.DeepEqual(got.Entries["other"].Extra, original.Entries["other"].Extra) || got.Artifacts["red"].ItemID != 3 {
					t.Fatal("unrelated data lost")
				}
			})
		}
	})
}
