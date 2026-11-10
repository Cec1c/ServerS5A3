package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type collectingGiveUpTransaction func(context.Context, string, func(dnfrepo.Group) error) error

func (f collectingGiveUpTransaction) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	return f(ctx, id, fn)
}

type collectingFailQuestSave struct{ dnfrepo.QuestRepository }

func (r collectingFailQuestSave) Save(context.Context, dnfrepo.QuestRecord) error {
	return errors.New("quest write failure after inventory staging")
}

type collectingFailInventorySave struct{ dnfrepo.InventoryRepository }

func (r collectingFailInventorySave) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("inventory write failure")
}

func TestCollectingGiveUpAtomicRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                                  string
		stageFailure                                                                                          string
		held, keep                                                                                            int64
		many, sharedTwice, split                                                                              bool
		material, rollback, wrongOwner, missingDefinition, noTransaction, cant, grant, malformed, unknownItem bool
	}{
		{name: "zero"}, {name: "partial", held: 5}, {name: "complete", held: 12}, {name: "excess", held: 36},
		{name: "shared", held: 36, keep: 7}, {name: "shared_below_requirement", held: 5, keep: 7},
		{name: "ordinary_retained", held: 36, material: true}, {name: "transaction_rollback", held: 36, rollback: true},
		{name: "account_mismatch", held: 36, wrongOwner: true}, {name: "unknown_other_active", held: 36, missingDefinition: true},
		{name: "transaction_missing", held: 36, noTransaction: true}, {name: "cant_giveup", held: 36, cant: true},
		{name: "activation_grant_closed", held: 36, grant: true}, {name: "malformed_closed", held: 36, malformed: true},
		{name: "metadata_missing", held: 36, unknownItem: true},
		{name: "inventory_write_failure", held: 36, stageFailure: "inventory"},
		{name: "quest_write_failure_after_inventory", held: 36, stageFailure: "quest"},
		{name: "twenty_two_active", many: true},
		{name: "multiple_shared_quests", held: 36, keep: 7, sharedTwice: true},
		{name: "shared_across_stacks", held: 36, keep: 7, split: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := dnfrepomemory.NewMemoryGroup()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "18", AccountID: "acc-quest"}))
			q := dnfrepo.QuestRecord{CharacterID: "18", States: map[int64]dnfrepo.QuestState{3145: {Status: "active"}, 3999: {Status: "completed"}}, Progress: map[int64]dnfrepo.QuestState{3145: {Status: "active"}}}
			catalog := testAcceptCatalog(t, "[type]\n`[seeking]`\n[int data]\n4730 12\n")
			d := catalog.byID[3145]
			d.CantGiveUp = tc.cant
			d.HasDependGiveItem = tc.grant
			if tc.malformed {
				d.IntData = []int64{4730, 12, 999}
			}
			catalog.byID[3145] = d
			if tc.keep > 0 {
				q.States[3146] = dnfrepo.QuestState{Status: "active"}
				catalog.byID[3146] = Definition{ID: 3146, Type: "[seeking]", IntData: []int64{4730, tc.keep}}
			}
			if tc.missingDefinition {
				q.States[3333] = dnfrepo.QuestState{Status: "active"}
			}
			if tc.many {
				for id := int64(4000); id < 4021; id++ {
					q.States[id] = dnfrepo.QuestState{Status: "active"}
					catalog.byID[id] = Definition{ID: id, Type: "[kill monster]"}
				}
			}
			if tc.sharedTwice {
				q.Progress[3147] = dnfrepo.QuestState{Status: "active"}
				catalog.byID[3147] = Definition{ID: 3147, Type: "[seeking]", IntData: []int64{4730, 3}}
			}
			must(r.Quest.Save(ctx, q))
			bag := dnfrepo.InventoryRecord{CharacterID: "18", Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: 1001, Count: 99}}}
			if tc.held > 0 {
				bag.Slots["0:161"] = dnfrepo.ItemStack{ItemID: 4730, Count: tc.held}
			}
			if tc.split {
				bag.Slots["0:161"] = dnfrepo.ItemStack{ItemID: 4730, Count: 2}
				bag.Slots["0:162"] = dnfrepo.ItemStack{ItemID: 4730, Count: tc.held - 2}
			}
			must(r.Inventory.Save(ctx, bag))
			original := r.CharacterSettlement
			if tc.stageFailure != "" {
				r.CharacterSettlement = collectingGiveUpTransaction(func(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
					return original.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
						if tc.stageFailure == "inventory" {
							tx.Inventory = collectingFailInventorySave{tx.Inventory}
						} else {
							tx.Quest = collectingFailQuestSave{tx.Quest}
						}
						return fn(tx)
					})
				})
			}
			if tc.rollback {
				r.CharacterSettlement = collectingGiveUpTransaction(func(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
					return original.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
						if err := fn(tx); err != nil {
							return err
						}
						return errors.New("injected before commit")
					})
				})
			}
			if tc.noTransaction {
				r.CharacterSettlement = nil
			}
			o, err := NewOwner(r)
			must(err)
			cmd := NewQuestIDCommand(alignedRequest("acc-quest", 18), "giveup_quest", QuestIDRequest{QuestID: 3145})
			if tc.wrongOwner {
				cmd.AccountID = "someone-else"
			}
			resolve := func(int64) (string, error) {
				if tc.unknownItem {
					return "", errors.New("missing metadata")
				}
				if tc.material {
					return "[material]", nil
				}
				return "[quest]", nil
			}
			result, err := o.ApplyCollectingGiveUp(ctx, catalog, cmd, resolve)
			failed := tc.rollback || tc.wrongOwner || tc.missingDefinition || tc.noTransaction || tc.cant || tc.grant || tc.malformed || tc.unknownItem || tc.stageFailure != ""
			after, _, e := r.Quest.Load(ctx, "18")
			must(e)
			items, _, e := r.Inventory.Load(ctx, "18")
			must(e)
			if failed {
				if err == nil || !reflect.DeepEqual(after, q) || !reflect.DeepEqual(items, bag) || result.QuestID != 0 {
					t.Fatalf("failed mutation err=%v result=%+v q=%+v bag=%+v", err, result, after, items)
				}
				return
			}
			must(err)
			if _, ok := after.States[3145]; ok {
				t.Fatal("active state remains")
			}
			if _, ok := after.Progress[3145]; ok {
				t.Fatal("legacy progress remains")
			}
			want := min(tc.keep, tc.held)
			if tc.material {
				want = tc.held
			}
			if items.Slots["0:161"].Count+items.Slots["0:162"].Count != want || items.Slots["0:121"].Count != 99 || after.States[3999].Status != "completed" {
				t.Fatalf("wrong assets/state: %+v %+v", items, after)
			}
			if tc.many {
				for id := int64(4000); id < 4021; id++ {
					if after.States[id].Status != "active" {
						t.Fatal("unrelated active quest changed")
					}
				}
			}
			if tc.sharedTwice && after.Progress[3147].Status != "active" {
				t.Fatal("legacy shared quest changed")
			}
			if tc.keep > 0 && after.States[3146].Status != "active" {
				t.Fatal("shared quest lost")
			}
			_, err = o.ApplyCollectingGiveUp(ctx, catalog, cmd, resolve)
			if !errors.Is(err, ErrQuestNotActive) {
				t.Fatalf("replay=%v", err)
			}
		})
	}
}
