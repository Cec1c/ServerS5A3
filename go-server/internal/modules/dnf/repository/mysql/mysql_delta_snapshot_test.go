package mysql

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestInventorySlotDeltaThenFieldsPreservesDirtyAndFailedSnapshots(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%t", fail), func(t *testing.T) {
			state, _, router := newDeathDeltaSQL(t)
			seed := questDeltaSeedInventory(t, router)
			store := newMySQLGroupFromRouter(router, false).Inventory.(*mysqlBagStore)
			store.trackSlotChanges = true
			record, _, err := store.Load(context.Background(), seed.CharacterID)
			if err != nil {
				t.Fatal(err)
			}
			original := repository.CloneInventory(*store.loadedSnapshot)
			for _, key := range []string{"0:184", "0:185"} {
				item := record.Slots[key]
				item.Count++
				record.Slots[key] = item
			}
			if fail {
				state.failAt = state.writes + 2
			}
			err = store.SaveItemStackSlotDelta(context.Background(), record, "slots", []string{"0:184"})
			if fail {
				if err == nil || !reflect.DeepEqual(original, *store.loadedSnapshot) {
					t.Fatal("failed SQL advanced snapshot", err)
				}
				loaded, _, loadErr := store.Load(context.Background(), seed.CharacterID)
				if loadErr != nil || !reflect.DeepEqual(original, loaded) {
					t.Fatal("failed slot write escaped rollback", loadErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveFields(context.Background(), record, repository.InventoryFieldSlots); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := store.Load(context.Background(), seed.CharacterID)
			if err != nil || !reflect.DeepEqual(record, loaded) {
				t.Fatal("partial save swallowed remaining dirty slot", err)
			}
		})
	}
}

func TestInventoryDeltaSnapshotOnlyAdvancesPersistedKeys(t *testing.T) {
	for _, field := range []repository.InventoryField{repository.InventoryFieldSlots, repository.InventoryFieldWarehouse} {
		t.Run(string(field), func(t *testing.T) {
			initial := repository.InventoryRecord{CharacterID: "c", Slots: map[string]repository.ItemStack{}, Warehouse: map[string]repository.ItemStack{}}
			for _, collection := range []map[string]repository.ItemStack{initial.Slots, initial.Warehouse} {
				for _, key := range []string{"saved", "dirty", "removed", "unsaved-remove"} {
					collection[key] = repository.ItemStack{ItemID: 1, Count: 2, RawEntry: []byte{1}, Extra: map[string]string{"value": "old"}}
				}
			}
			record := repository.CloneInventory(initial)
			store := &mysqlBagStore{loadedSnapshot: &initial}
			values, baseline, other := record.Slots, initial.Slots, initial.Warehouse
			if field == repository.InventoryFieldWarehouse {
				values, baseline, other = record.Warehouse, initial.Warehouse, initial.Slots
			}
			for _, key := range []string{"saved", "dirty", "inserted"} {
				values[key] = repository.ItemStack{ItemID: 2, Count: 3, RawEntry: []byte{2}, Extra: map[string]string{"value": "new"}}
			}
			delete(values, "removed")
			delete(values, "unsaved-remove")
			store.rememberAccountTransactionKeys(record, field, []string{"saved", "removed", "inserted"})
			values["saved"].RawEntry[0] = 9
			values["saved"].Extra["value"] = "later"
			if baseline["saved"].RawEntry[0] != 2 || baseline["saved"].Extra["value"] != "new" || baseline["inserted"].Count != 3 {
				t.Fatal("saved snapshot aliases caller or lost insert")
			}
			if _, ok := baseline["removed"]; ok {
				t.Fatal("saved removal lost")
			}
			if baseline["dirty"].ItemID != 1 || baseline["unsaved-remove"].Count != 2 || other["saved"].ItemID != 1 {
				t.Fatal("unsaved changes advanced baseline")
			}
		})
	}
}

// Compare the original PR's full clone with the bounded snapshot using the
// same single-slot change. This measures snapshot work, not end-to-end latency.
func BenchmarkInventoryDeltaSnapshot(b *testing.B) {
	for _, size := range []int{64, 512} {
		for _, fullClone := range []bool{true, false} {
			b.Run(fmt.Sprintf("items=%d/fullClone=%t", size, fullClone), func(b *testing.B) {
				record := repository.InventoryRecord{CharacterID: "c", Slots: map[string]repository.ItemStack{}, Warehouse: map[string]repository.ItemStack{}}
				for i := 0; i < size; i++ {
					item := repository.ItemStack{ItemID: int64(i), Count: 2, RawEntry: make([]byte, 64), Extra: map[string]string{"a": "1", "b": "2"}}
					record.Slots[fmt.Sprint(i)] = item
					record.Warehouse[fmt.Sprint(i)] = item
				}
				snapshot := repository.CloneInventory(record)
				store := &mysqlBagStore{loadedSnapshot: &snapshot}
				keys := []string{"0"}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if fullClone {
						copy := repository.CloneInventory(record)
						snapshot.Slots["0"] = copy.Slots["0"]
					} else {
						store.rememberAccountTransactionKeys(record, repository.InventoryFieldSlots, keys)
					}
				}
			})
		}
	}
}
