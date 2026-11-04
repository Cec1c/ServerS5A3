package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestMySQLAssetStateDeltaPreservesFullSaveAndRollback(t *testing.T) {
	for _, scenario := range []string{"single_stat", "delete_stat", "permanent_receipt", "second_save", "write_failure", "no_snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			var snapshots []map[string][]string
			var counts []int
			for _, optimized := range []bool{false, true} {
				state, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaState(t, router)
				initial := deathDeltaSnapshot(state)
				state.writes = 0
				state.commits = 0
				if scenario == "write_failure" {
					state.failAt = 2
				}
				err := (&mysqlCharacterAssetUnitOfWork{router: router}).WithinCharacterAssets(context.Background(), "77", func(chars repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository) error {
					store := chars.(*mysqlCharStore)
					if !optimized {
						store.trackStatChanges = false
					}
					c, _, err := chars.Load(context.Background(), "77")
					if err != nil {
						return err
					}
					if scenario == "no_snapshot" {
						store.loadedSnapshot = nil
					}
					c.Stats["new_activity_counter"] = 1
					if scenario == "delete_stat" {
						delete(c.Stats, "角色无关项1")
					}
					if scenario == "permanent_receipt" {
						delete(c.Stats, repository.LevelUpEventKey(80))
						c.Stats[repository.LevelUpEventKey(90)] = 12345
					}
					if err := repository.SaveCharacterFields(context.Background(), chars, c, repository.CharacterFieldStats); err != nil {
						return err
					}
					if scenario == "second_save" {
						c.Stats["new_activity_counter"] = 2
						return repository.SaveCharacterFields(context.Background(), chars, c, repository.CharacterFieldStats)
					}
					return nil
				})
				if scenario == "write_failure" {
					if err == nil || state.commits != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(state)) {
						t.Fatal("transaction did not roll back", err)
					}
				} else if err != nil || state.commits != 1 {
					t.Fatal("asset transaction", err)
				}
				counts = append(counts, state.writes)
				snapshots = append(snapshots, deathDeltaSnapshot(state))
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("full and delta rows differ")
			}
			if scenario == "single_stat" && (counts[0] < 80 || counts[1] != 2) {
				t.Fatalf("single counter still rewrites unrelated stats: %v", counts)
			}
			t.Logf("%s SQL writes %d -> %d, relational rows equal", scenario, counts[0], counts[1])
		})
	}
}
