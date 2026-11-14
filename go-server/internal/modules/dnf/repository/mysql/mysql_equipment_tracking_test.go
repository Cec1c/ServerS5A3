package mysql

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strings"
	"testing"
)

func TestEquipmentTrackingMatchesFullSaveWithoutRewritingOtherSlots(t *testing.T) {
	var results []map[string][]string
	for _, tracked := range []bool{false, true} {
		db := newRowModelDB()
		g := newRowModelGroup(t, db, true)
		record := r.EquipmentRecord{CharacterID: "77", Entries: map[string]r.EquipmentEntry{
			"weapon": {ItemID: 1, RawEntry: rowModelRawEntry(1), Extra: map[string]string{"durability": "90"}},
			"coat":   {ItemID: 2, RawEntry: rowModelRawEntry(2)},
		}}
		if err := g.Equipment.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if tracked {
			enableInventoryDeltaTracking(g)
		}
		record, _, err := g.Equipment.Load(context.Background(), "77")
		if err != nil {
			t.Fatal(err)
		}
		db.calls = nil
		weapon := record.Entries["weapon"]
		weapon.Extra["durability"] = "89"
		weapon.RawEntry[3]++
		record.Entries["weapon"] = weapon
		if err := g.Equipment.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if tracked {
			for _, call := range db.calls {
				if strings.HasPrefix(call.Query, "SELECT") {
					continue
				}
				for _, arg := range call.Args {
					if arg == "coat" {
						t.Fatal("unchanged equipment rewritten")
					}
				}
			}
		}
		results = append(results, db.model.snapshot())
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("delta changed saved equipment")
	}
}
