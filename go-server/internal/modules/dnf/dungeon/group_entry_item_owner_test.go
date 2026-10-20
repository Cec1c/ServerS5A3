package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestEntryItemGroupAtomicAlternatives(t *testing.T) {
	for _, scenario := range []string{"mixed_pass_and_tickets", "late_member_short", "wrong_account", "projector_failure", "invalid_alternative"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			before := map[string]dnfrepo.InventoryRecord{}
			for _, id := range []string{"1", "6"} {
				if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id}); err != nil {
					t.Fatal(err)
				}
				slots := map[string]dnfrepo.ItemStack{"0:121": {ItemID: 100, Count: 40}, "0:122": {ItemID: 100, Count: 5}, "1:1": {ItemID: 100, Count: 999}}
				if id == "1" {
					slots["0:70"] = dnfrepo.ItemStack{ItemID: 200, Count: 1}
				}
				if scenario == "late_member_short" && id == "6" {
					slots["0:121"] = dnfrepo.ItemStack{ItemID: 100, Count: 20}
					delete(slots, "0:122")
				}
				record := dnfrepo.InventoryRecord{CharacterID: id, Slots: slots, Warehouse: map[string]dnfrepo.ItemStack{"0:1": {ItemID: 100, Count: 999}}}
				if err := repos.Inventory.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
				before[id] = dnfrepo.CloneInventory(record)
			}
			alternatives := [][]EntryItemRequirement{{{ItemID: 200, Count: 1, ConsumeOnEntry: true}}, {{ItemID: 100, Count: 28, ConsumeOnEntry: true}}}
			cmd := EntryItemGroupCommand{Members: []EntryItemGroupMember{{CharacterID: "1", AccountID: "account-1", Alternatives: alternatives}, {CharacterID: "6", AccountID: "account-6", Alternatives: alternatives}}}
			if scenario == "wrong_account" {
				cmd.Members[1].AccountID = "account-1"
			}
			if scenario == "projector_failure" {
				cmd.Project = func(int16, dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
					return dnfrepo.ItemStack{}, errors.New("projection failed")
				}
			}
			if scenario == "invalid_alternative" {
				cmd.Members[1].Alternatives = [][]EntryItemRequirement{{{ItemID: 100, Count: 0, ConsumeOnEntry: true}}}
			}
			result, err := ConsumeEntryItemGroup(ctx, repos, cmd)
			if scenario != "mixed_pass_and_tickets" {
				if err == nil || result != nil {
					t.Fatalf("failure returned success: %+v %v", result, err)
				}
				for id, original := range before {
					actual, _, _ := repos.Inventory.Load(ctx, id)
					if !reflect.DeepEqual(actual, original) {
						t.Fatalf("partial debit for %s: %+v", id, actual)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			one, _, _ := repos.Inventory.Load(ctx, "1")
			six, _, _ := repos.Inventory.Load(ctx, "6")
			if _, ok := one.Slots["0:70"]; ok || one.Slots["0:121"].Count != 40 || six.Slots["0:121"].Count != 12 || six.Slots["0:122"].Count != 5 || six.Slots["1:1"].Count != 999 || six.Warehouse["0:1"].Count != 999 {
				t.Fatalf("incorrect debit: %+v %+v", one, six)
			}
			if result["1"].Consumed[0].ItemID != 200 || result["6"].Consumed[0].Count != 28 {
				t.Fatalf("bad receipts: %+v", result)
			}
		})
	}
}
