package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// Record the owner's actual delta and model the existing SQL writer's
// conflict result after a concurrent target change/removal. SQL CAS behavior
// itself is covered by repository integration tests.
type ownerLateQuestDeltaSpy struct {
	r.QuestRepository
	before, after r.QuestRecord
	fields        []r.QuestField
	calls         int
	concurrent    func(context.Context) error
}

func (s *ownerLateQuestDeltaSpy) SaveQuestChanges(ctx context.Context, before, after r.QuestRecord, fields ...r.QuestField) error {
	s.calls++
	s.before, s.after = r.CloneQuest(before), r.CloneQuest(after)
	s.fields = append([]r.QuestField(nil), fields...)
	if err := s.concurrent(ctx); err != nil {
		return err
	}
	return r.ErrQuestVersionConflict
}

func TestOwnerGiveUpDeltaPreservesWeddingRowsAndReturnsTargetConflict(t *testing.T) {
	for _, conflict := range []string{"changed", "missing"} {
		t.Run(conflict, func(t *testing.T) {
			ctx := context.Background()
			group := memory.NewMemoryGroup()
			if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary"}); err != nil {
				t.Fatal(err)
			}
			before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{
				3145: {Status: "active", ProgressValue: 1}, 20737: {Status: "active", ProgressValue: 5},
			}, Progress: map[int64]r.QuestState{3145: {Status: "active", ProgressValue: 2}, 20738: {Status: "active", ProgressValue: 1}}}
			if err := group.Quest.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			base := group.Quest
			spy := &ownerLateQuestDeltaSpy{QuestRepository: base, concurrent: func(ctx context.Context) error {
				current := r.CloneQuest(before)
				if conflict == "missing" {
					delete(current.States, 3145)
					delete(current.Progress, 3145)
				} else {
					state := current.States[3145]
					state.ProgressValue = 9
					current.States[3145] = state
				}
				return base.Save(ctx, current)
			}}
			group.Quest = spy
			owner, _ := NewOwner(group)
			result, err := owner.ApplyGiveUp(ctx, testAcceptCatalog(t, "[type]\n`[clear map]`\n[int data]\n76126\n"), Command{AccountID: "ordinary", SelectedCharacterID: 19, QuestID: 3145})
			if !errors.Is(err, r.ErrQuestVersionConflict) || result.QuestID != 0 || spy.calls != 1 {
				t.Fatalf("give-up conflict result=%+v err=%v calls=%d", result, err, spy.calls)
			}
			if !reflect.DeepEqual(spy.before, before) || !reflect.DeepEqual(spy.fields, []r.QuestField{r.QuestFieldStates, r.QuestFieldProgress}) {
				t.Fatal("give-up did not pass its real before-image and both fields")
			}
			if !reflect.DeepEqual(r.ChangedQuestIDs(spy.before.States, spy.after.States), []int64{3145}) || !reflect.DeepEqual(r.ChangedQuestIDs(spy.before.Progress, spy.after.Progress), []int64{3145}) {
				t.Fatal("give-up delta changed unrelated tasks")
			}
			if !r.QuestStatesEqual(spy.before.States[20737], spy.after.States[20737]) || !r.QuestStatesEqual(spy.before.Progress[20738], spy.after.Progress[20738]) {
				t.Fatal("give-up changed wedding preparation")
			}
			actual, _, _ := base.Load(ctx, "19")
			if conflict == "missing" {
				if _, ok := actual.States[3145]; ok {
					t.Fatal("stale give-up restored removed target")
				}
			} else if actual.States[3145].ProgressValue != 9 {
				t.Fatal("stale give-up overwrote concurrent target change")
			}
		})
	}
}

func TestOwnerSetTriggerDeltaUsesBeforeMetadataAndReturnsTargetConflict(t *testing.T) {
	for _, conflict := range []string{"changed", "missing"} {
		t.Run(conflict, func(t *testing.T) {
			ctx := context.Background()
			group := memory.NewMemoryGroup()
			if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary"}); err != nil {
				t.Fatal(err)
			}
			if err := group.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:8": {ItemID: 10002610, Count: 1}}}); err != nil {
				t.Fatal(err)
			}
			before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{
				20737: {Status: "active", ProgressValue: 5},
				20738: {Status: "active", ProgressValue: 1, Extra: map[string]string{"quest_type": "[seeking]", "seeking_item_ids": "10002610"}},
			}}
			if err := group.Quest.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			base := group.Quest
			spy := &ownerLateQuestDeltaSpy{QuestRepository: base, concurrent: func(ctx context.Context) error {
				current := r.CloneQuest(before)
				if conflict == "missing" {
					delete(current.States, 20738)
				} else {
					state := current.States[20738]
					state.ProgressValue = 9
					current.States[20738] = state
				}
				return base.Save(ctx, current)
			}}
			group.Quest = spy
			owner, _ := NewOwner(group)
			result, err := owner.ApplySetTrigger(ctx, Command{AccountID: "ordinary", SelectedCharacterID: 19, QuestID: 20738, Operation: "set_quest_trigger", TriggerType: 1})
			if !errors.Is(err, r.ErrQuestVersionConflict) || result.StateChanged || spy.calls != 1 {
				t.Fatalf("trigger conflict result=%+v err=%v calls=%d", result, err, spy.calls)
			}
			if !reflect.DeepEqual(spy.before, before) || !reflect.DeepEqual(spy.fields, []r.QuestField{r.QuestFieldStates}) {
				t.Fatal("trigger before-image was modified along with after metadata")
			}
			if !reflect.DeepEqual(r.ChangedQuestIDs(spy.before.States, spy.after.States), []int64{20738}) || !r.QuestStatesEqual(spy.before.States[20737], spy.after.States[20737]) {
				t.Fatal("trigger delta affected unrelated wedding task")
			}
			if spy.after.States[20738].ProgressValue != 0 || spy.after.States[20738].Extra["last_set_trigger_type"] != "1" {
				t.Fatal("trigger after-image lost material progress")
			}
			actual, _, _ := base.Load(ctx, "19")
			if conflict == "missing" {
				if _, ok := actual.States[20738]; ok {
					t.Fatal("stale trigger restored cancelled preparation")
				}
			} else if actual.States[20738].ProgressValue != 9 {
				t.Fatal("stale trigger overwrote concurrent target change")
			}
		})
	}
}
