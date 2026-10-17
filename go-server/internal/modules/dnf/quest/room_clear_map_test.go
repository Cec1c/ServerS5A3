package quest

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPlanRoomClearMapCompletionMatchesQST002ShapeAndRecomputesParent(t *testing.T) {
	catalog := roomClearMapTestCatalog(t)
	completedAt := time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run-19/room-82/92247",
		CompletedAt:    completedAt,
	}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3525: {Status: "active", ProgressValue: 1, Extra: map[string]string{"parent_kept": "yes"}},
			3558: {Status: "completed", ProgressValue: 0},
			3559: {Status: "completed", ProgressValue: 0},
			3560: {Status: "active", ProgressValue: 1, Extra: map[string]string{"child_kept": "yes"}},
			9999: {Status: "active", ProgressValue: 7, Extra: map[string]string{"unrelated": "yes"}},
		},
		Progress: map[int64]dnfrepo.QuestState{
			3526: {Status: "active", ProgressValue: 3},
		},
	}

	plan, err := catalog.PlanRoomClearMapCompletion(record, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Completions) != 1 || plan.Completions[0].QuestID != 3560 ||
		plan.Completions[0].PreviousTrigger != 1 || plan.Completions[0].Idempotent {
		t.Fatalf("completions = %+v", plan.Completions)
	}
	if want := []dnfrepo.QuestField{dnfrepo.QuestFieldStates}; !reflect.DeepEqual(plan.ChangedFields, want) {
		t.Fatalf("changed fields = %v, want %v", plan.ChangedFields, want)
	}
	child := plan.Record.States[3560]
	if child.Status != "completed" || child.ProgressValue != 0 || child.UpdatedAt != completedAt {
		t.Fatalf("completed child = %+v", child)
	}
	wantExtra := map[string]string{
		"child_kept":                         "yes",
		"completion_kind":                    "clear_map",
		"completion_source":                  "room_clear",
		"completion_key":                     input.CompletionKey,
		"completion_dungeon_id":              "82",
		"completion_map_id":                  "92247",
		"linked_objective_completion_source": "room_clear",
		"pvf_path":                           "n_quest/clear_3560.qst",
		"quest_type":                         "[clear map]",
		"reward_state":                       finishRewardGranted,
		"auto_completed":                     "true",
		"auto_complete_reason":               "no_reward_sub_quest",
	}
	if !reflect.DeepEqual(child.Extra, wantExtra) {
		t.Fatalf("child extra = %#v, want %#v", child.Extra, wantExtra)
	}
	parent := plan.Record.States[3525]
	if parent.Status != "active" || parent.ProgressValue != 0 || parent.Extra["parent_kept"] != "yes" {
		t.Fatalf("parent = %+v", parent)
	}
	if len(plan.ParentProgress) != 1 ||
		plan.ParentProgress[0].QuestID != 3525 ||
		plan.ParentProgress[0].PreviousTrigger != 1 ||
		plan.ParentProgress[0].ProgressValue != 0 {
		t.Fatalf("parent progress = %+v", plan.ParentProgress)
	}
	if unrelated := plan.Record.States[9999]; unrelated.ProgressValue != 7 || unrelated.Extra["unrelated"] != "yes" {
		t.Fatalf("unrelated state changed = %+v", unrelated)
	}
	if unrelated := plan.Record.Progress[3526]; unrelated.ProgressValue != 3 {
		t.Fatalf("unrelated progress field changed = %+v", unrelated)
	}

	if original := record.States[3560]; original.Status != "active" || original.ProgressValue != 1 ||
		original.Extra["completion_key"] != "" || original.Extra["child_kept"] != "yes" {
		t.Fatalf("input child mutated = %+v", original)
	}
	if original := record.States[3525]; original.ProgressValue != 1 {
		t.Fatalf("input parent mutated = %+v", original)
	}
}

func TestPlanRoomClearMapCompletionReplayDoesNotChangeRecord(t *testing.T) {
	catalog := roomClearMapTestCatalog(t)
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run-19/replay",
		CompletedAt:    time.Date(2026, 10, 18, 14, 1, 0, 0, time.UTC),
	}
	first, err := catalog.PlanRoomClearMapCompletion(dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3525: {Status: "active", ProgressValue: 1},
			3558: {Status: "completed", ProgressValue: 0},
			3559: {Status: "completed", ProgressValue: 0},
			3560: {Status: "active", ProgressValue: 1},
		},
	}, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := catalog.PlanRoomClearMapCompletion(first.Record, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Completions) != 1 || !replay.Completions[0].Idempotent {
		t.Fatalf("replay completions = %+v", replay.Completions)
	}
	if len(replay.ChangedFields) != 0 || len(replay.ParentProgress) != 0 {
		t.Fatalf("replay changed fields=%v parents=%+v", replay.ChangedFields, replay.ParentProgress)
	}
	if !reflect.DeepEqual(replay.Record, first.Record) {
		t.Fatalf("replay changed record\nfirst=%+v\nreplay=%+v", first.Record, replay.Record)
	}
}

func TestPlanRoomClearMapCompletionRecomputesNegativeParent(t *testing.T) {
	catalog := roomClearMapTestCatalog(t)
	plan, err := catalog.PlanRoomClearMapCompletion(dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3525: {Status: "active", ProgressValue: -1},
			3558: {Status: "completed", ProgressValue: 0},
			3559: {Status: "completed", ProgressValue: 0},
			3560: {Status: "active", ProgressValue: 1},
		},
	}, RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run-19/negative-parent",
		CompletedAt:    time.Date(2026, 10, 18, 14, 1, 30, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Completions) != 1 ||
		plan.Record.States[3560].Status != "completed" ||
		plan.Record.States[3525].Status != "active" ||
		plan.Record.States[3525].ProgressValue != 0 {
		t.Fatalf("negative parent plan = %+v", plan)
	}
}

func TestPlanRoomClearMapCompletionValidatesInput(t *testing.T) {
	catalog := roomClearMapTestCatalog(t)
	record := roomClearMapActiveRecord()
	base := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "valid",
		CompletedAt:    time.Now().UTC(),
	}
	tests := []struct {
		name string
		edit func(*RoomClearMapInput)
		want error
	}{
		{name: "dungeon", edit: func(in *RoomClearMapInput) { in.DungeonID = 0 }, want: ErrRoomClearMapDungeonRequired},
		{name: "map", edit: func(in *RoomClearMapInput) { in.MapID = 0 }, want: ErrRoomClearMapMapRequired},
		{name: "key", edit: func(in *RoomClearMapInput) { in.CompletionKey = "  " }, want: ErrRoomClearMapCompletionKeyRequired},
		{name: "time", edit: func(in *RoomClearMapInput) { in.CompletedAt = time.Time{} }, want: ErrRoomClearMapCompletionTimeRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := catalog.PlanRoomClearMapCompletion(record, input); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPlanRoomClearMapCompletionRejectsNonAuthoritativeMatches(t *testing.T) {
	catalog := roomClearMapTestCatalog(t)
	completedAt := time.Date(2026, 10, 18, 14, 2, 0, 0, time.UTC)
	tests := []struct {
		name   string
		record dnfrepo.QuestRecord
		input  RoomClearMapInput
	}{
		{
			name:   "only dungeon id matches",
			record: roomClearMapRecord(3526, 1, 3563, 1),
			input:  RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3563}, CompletionKey: "dungeon-only", CompletedAt: completedAt},
		},
		{
			name:   "wrong map",
			record: roomClearMapActiveRecord(),
			input:  RoomClearMapInput{DungeonID: 82, MapID: 92251, LinkedQuestIDs: []int64{3560}, CompletionKey: "wrong-map", CompletedAt: completedAt},
		},
		{
			name:   "child not bound by maze",
			record: roomClearMapActiveRecord(),
			input:  RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3558}, CompletionKey: "unbound", CompletedAt: completedAt},
		},
		{
			name: "reward child",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3525: {Status: "active", ProgressValue: 1},
				3561: {Status: "active", ProgressValue: 1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3561}, CompletionKey: "reward", CompletedAt: completedAt},
		},
		{
			name: "missing parent state",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3558: {Status: "completed", ProgressValue: 0},
				3559: {Status: "completed", ProgressValue: 0},
				3560: {Status: "active", ProgressValue: 1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3560}, CompletionKey: "missing-parent", CompletedAt: completedAt},
		},
		{
			name: "missing child state",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3525: {Status: "active", ProgressValue: 1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3560}, CompletionKey: "missing-child", CompletedAt: completedAt},
		},
		{
			name: "cross parent",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3527: {Status: "active", ProgressValue: 1},
				3564: {Status: "active", ProgressValue: 1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3564}, CompletionKey: "cross-parent", CompletedAt: completedAt},
		},
		{
			name: "negative child progress",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3525: {Status: "active", ProgressValue: 1},
				3558: {Status: "completed", ProgressValue: 0},
				3559: {Status: "completed", ProgressValue: 0},
				3560: {Status: "active", ProgressValue: -1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3560}, CompletionKey: "negative", CompletedAt: completedAt},
		},
		{
			name: "inactive parent",
			record: dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
				3525: {Status: "completed", ProgressValue: 0},
				3560: {Status: "active", ProgressValue: 1},
			}},
			input: RoomClearMapInput{DungeonID: 82, MapID: 92247, LinkedQuestIDs: []int64{3560}, CompletionKey: "inactive-parent", CompletedAt: completedAt},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := dnfrepo.CloneQuest(test.record)
			plan, err := catalog.PlanRoomClearMapCompletion(test.record, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Completions) != 0 || len(plan.ChangedFields) != 0 || len(plan.ParentProgress) != 0 {
				t.Fatalf("unexpected plan = %+v", plan)
			}
			if !reflect.DeepEqual(plan.Record, before) {
				t.Fatalf("record changed\nbefore=%+v\nafter=%+v", before, plan.Record)
			}
		})
	}
}

func TestRealScriptPVFRoomClearMapQST002Shape(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real QST-002 room clear-map shape")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	parent, parentFound := catalog.Find(3525)
	child, childFound := catalog.Find(3560)
	if !parentFound || !childFound ||
		!questClearDefinition(parent) ||
		!roomClearMapIntContains(parent.IntData, 3560) ||
		child.MainQuestID != 3525 ||
		!roomClearMapChildMatches(child, 92247) {
		t.Fatalf("QST-002 definitions: parent=%+v found=%t child=%+v found=%t", parent, parentFound, child, childFound)
	}
	plan, err := catalog.PlanRoomClearMapCompletion(dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3525: {Status: "active", ProgressValue: 1},
			3558: {Status: "completed", ProgressValue: 0},
			3559: {Status: "completed", ProgressValue: 0},
			3560: {Status: "active", ProgressValue: 1},
		},
	}, RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "real-pvf/qst-002/82/92247",
		CompletedAt:    time.Date(2026, 10, 18, 14, 3, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Completions) != 1 ||
		plan.Completions[0].QuestID != 3560 ||
		plan.Record.States[3560].Status != "completed" ||
		plan.Record.States[3525].ProgressValue != 0 {
		t.Fatalf("real QST-002 plan = %+v", plan)
	}
	t.Logf("real QST-002 child path=%q map=%d parent=%d", plan.Completions[0].PVFPath, 92247, plan.Record.States[3525].ProgressValue)
}

func roomClearMapActiveRecord() dnfrepo.QuestRecord {
	return dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3525: {Status: "active", ProgressValue: 1},
			3558: {Status: "completed", ProgressValue: 0},
			3559: {Status: "completed", ProgressValue: 0},
			3560: {Status: "active", ProgressValue: 1},
		},
	}
}

func roomClearMapRecord(parentID int64, parentProgress int64, childID int64, childProgress int64) dnfrepo.QuestRecord {
	return dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			parentID: {Status: "active", ProgressValue: parentProgress},
			childID:  {Status: "active", ProgressValue: childProgress},
		},
	}
}

func roomClearMapTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "3525 `parent_3525.qst`\n3526 `parent_3526.qst`\n3527 `parent_3527.qst`\n" +
			"3558 `meet_3558.qst`\n3559 `meet_3559.qst`\n3560 `clear_3560.qst`\n" +
			"3561 `reward_3561.qst`\n3563 `dungeon_only_3563.qst`\n3564 `cross_parent_3564.qst`\n",
		"n_quest/parent_3525.qst": questCatalogTestDefinition("[epic]", 65, 99, "[all]",
			"[type]\n`[quest clear]`\n[sub type]\n2\n[int data]\n3558 3559 3560\n"),
		"n_quest/parent_3526.qst": questCatalogTestDefinition("[epic]", 65, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n3563\n"),
		"n_quest/parent_3527.qst": questCatalogTestDefinition("[epic]", 65, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n3564\n"),
		"n_quest/meet_3558.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[meet npc]`\n[main quest]\n3525\n[int data]\n59\n"),
		"n_quest/meet_3559.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[meet npc]`\n[main quest]\n3525\n[int data]\n58\n"),
		"n_quest/clear_3560.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3525\n[int data]\n92247\n"),
		"n_quest/reward_3561.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3525\n[int data]\n92247\n[reward type]\n`[item]`\n[reward int data]\n1000 1\n"),
		"n_quest/dungeon_only_3563.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3526\n[int data]\n82\n"),
		"n_quest/cross_parent_3564.qst": questCatalogTestDefinition("[sub]", 65, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3526\n[int data]\n92247\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
