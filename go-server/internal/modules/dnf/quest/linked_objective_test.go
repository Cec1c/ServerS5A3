package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func linkedObjectiveTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList:              "3249 `parent3249.qst`\n3609 `meet.qst`\n3610 `seekmeet.qst`\n3347 `parent3347.qst`\n3425 `hunt.qst`\n3426 `seeking.qst`\n3430 `ordinary.qst`\n",
		"n_quest/parent3249.qst": "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[int data]\n3609 3610\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/meet.qst":       "[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[meet npc]`\n[main quest]\n3249\n[int data]\n303\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/seekmeet.qst":   "[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seek n meet npc]`\n[main quest]\n3249\n[int data]\n3037 1 309\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/parent3347.qst": "[grade]\n`[epic]`\n[type]\n`[quest clear]`\n[int data]\n3425 3426\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/hunt.qst":       "[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[hunt enemy]`\n[main quest]\n3347\n[int data]\n3 -1 13099 3 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/seeking.qst":    "[grade]\n`[sub]`\n[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seeking]`\n[main quest]\n3347\n[int data]\n3037 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
		"n_quest/ordinary.qst":   "[grade]\n`[normal]`\n[type]\n`[hunt enemy]`\n[int data]\n3 -1 13099 3 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n",
	}
	index, err := pvf.Build(context.Background(), source, pvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestPlanLinkedObjectiveProgressCompletesNoRewardChildrenAndKeepsDungeonOwners(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	now := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3347: {Status: "active", ProgressValue: 1},
		3425: {Status: "active", ProgressValue: 1},
		3426: {Status: "active", ProgressValue: 0},
	}}
	town, err := catalog.PlanTownLinkedProgress(record, 3347, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(town.CompletedQuestIDs, []int64{3426}) || town.ParentQuestID != 3347 || town.ParentProgress != 1 {
		t.Fatalf("town plan=%+v", town)
	}
	if state := town.Record.States[3426]; state.Status != "completed" || state.Extra["reward_state"] != finishRewardGranted {
		t.Fatalf("3426 state=%+v", state)
	}

	town.Record.States[3425] = dnfrepo.QuestState{Status: "active", ProgressValue: 0}
	finish, err := catalog.PlanLinkedObjectiveFinish(town.Record, 3425, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(finish.CompletedQuestIDs, []int64{3425}) || finish.ParentProgress != 0 || finish.Record.States[3347].ProgressValue != 0 {
		t.Fatalf("finish plan=%+v", finish)
	}
}

func TestPlanTownQuestClearParentCallbackReconcilesWithoutArchivingUnresolvedChild(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		parentProgress  int64
		childProgress   int64
		wantParent      int64
		wantChanged     bool
		wantChildStatus string
	}{
		{name: "already-correct", parentProgress: 1, childProgress: 1, wantParent: 1, wantChildStatus: "active"},
		{name: "legacy-parent-zero", parentProgress: 0, childProgress: 1, wantParent: 1, wantChanged: true, wantChildStatus: "active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3347: {Status: "active", ProgressValue: test.parentProgress},
				3425: {Status: "active", ProgressValue: test.childProgress},
				3426: {Status: "completed", ProgressValue: 0},
			}}
			plan, err := catalog.PlanTownLinkedProgress(record, 3347, now)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Handled || plan.ParentQuestID != 3347 || plan.ParentProgress != test.wantParent {
				t.Fatalf("plan=%+v", plan)
			}
			if len(plan.CompletedQuestIDs) != 0 || plan.Record.States[3425].Status != test.wantChildStatus {
				t.Fatalf("unresolved clear-map child was archived: plan=%+v state=%+v", plan, plan.Record.States[3425])
			}
			if plan.Record.States[3347].ProgressValue != test.wantParent ||
				(len(plan.ChangedFields) != 0) != test.wantChanged {
				t.Fatalf("parent projection=%+v fields=%v", plan.Record.States[3347], plan.ChangedFields)
			}
		})
	}
}

func TestPlanTownQuestClearParentCallbackKeepsTerminalChildOwnership(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3347: {Status: "active", ProgressValue: 1},
		3425: {Status: "active", ProgressValue: 0},
		3426: {Status: "active", ProgressValue: 0},
	}}
	plan, err := catalog.PlanTownLinkedProgress(record, 3347, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Handled || !reflect.DeepEqual(plan.CompletedQuestIDs, []int64{3426}) {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Record.States[3425].Status != "active" || plan.Record.States[3425].ProgressValue != 0 {
		t.Fatalf("clear-map child must retain op34 ownership: %+v", plan.Record.States[3425])
	}
	if plan.Record.States[3426].Status != "completed" || plan.ParentProgress != 0 {
		t.Fatalf("town terminal child was not archived: plan=%+v", plan)
	}
}

func TestPlanTownLeavesOrdinaryTriggerForOwnerFallback(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3430: {Status: "active", ProgressValue: 1},
	}}
	plan, err := catalog.PlanTownLinkedProgress(record, 3430, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Handled || len(plan.ChangedFields) != 0 || plan.ParentQuestID != 0 {
		t.Fatalf("ordinary trigger was linked: %+v", plan)
	}
}

func TestOwnerApplyLinkedObjectiveFinishCommitsQuestOnly(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Level: 60}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3347: {Status: "active", ProgressValue: 1},
		3425: {Status: "active", ProgressValue: 0},
		3426: {Status: "completed", ProgressValue: 0},
	}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyLinkedObjectiveFinish(ctx, catalog, "19", 3425, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.ParentProgress != 0 || !reflect.DeepEqual(result.CompletedQuestIDs, []int64{3425}) {
		t.Fatalf("commit=%+v", result)
	}
	stored, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found || stored.States[3425].Status != "completed" || stored.States[3347].ProgressValue != 0 {
		t.Fatalf("stored=%+v found=%t err=%v", stored, found, err)
	}
}

type linkedObjectiveRejectingSettlement struct {
	err  error
	base dnfrepo.CharacterSettlementUnitOfWork
}

func (s linkedObjectiveRejectingSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return s.base.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return s.err
	})
}

func TestOwnerTownParentReconcileHonorsTransactionFailure(t *testing.T) {
	catalog := linkedObjectiveTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1"}); err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3347: {Status: "active", ProgressValue: 0},
		3425: {Status: "active", ProgressValue: 1},
		3426: {Status: "completed", ProgressValue: 0},
	}}
	if err := repositories.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("reject linked objective transaction")
	repositories.CharacterSettlement = linkedObjectiveRejectingSettlement{err: wantErr, base: repositories.CharacterSettlement}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyTownLinkedProgress(ctx, catalog, "19", 3347, time.Now().UTC()); !errors.Is(err, wantErr) {
		t.Fatalf("error=%v want=%v", err, wantErr)
	}
	stored, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load stored found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(stored, record) {
		t.Fatalf("transaction failure changed quest: got=%+v want=%+v", stored, record)
	}
}
