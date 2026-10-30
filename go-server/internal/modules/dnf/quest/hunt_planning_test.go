package quest

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestHuntPlanningDependencyIDsUsesOnlyActiveQuestClearDefinitions(t *testing.T) {
	source := catalogTestSource{
		DefaultList: "10 `root.qst`\n20 `child.qst`\n30 `leaf.qst`\n40 `nested_nonclear.qst`\n50 `second.qst`\n60 `second_leaf.qst`\n70 `alias.qst`\n80 `alias_nonclear.qst`\n90 `active_nonclear.qst`\n674 `history.qst`\n",
		"n_quest/root.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n20 50 -7 20\n"),
		"n_quest/child.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n30 -1 30\n"),
		"n_quest/leaf.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[clear quest]`\n[int data]\n20 40\n"),
		"n_quest/nested_nonclear.qst": questCatalogTestDefinition("[normal]", 1, 99, "[all]",
			"[type]\n`[hunt enemy]`\n[int data]\n900\n"),
		"n_quest/second.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n60 60\n"),
		"n_quest/second_leaf.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n20\n"),
		"n_quest/alias.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n80\n"),
		"n_quest/alias_nonclear.qst": questCatalogTestDefinition("[normal]", 1, 99, "[all]",
			"[type]\n`[use item]`\n[int data]\n901\n"),
		"n_quest/active_nonclear.qst": questCatalogTestDefinition("[normal]", 1, 99, "[all]",
			"[type]\n`[get item]`\n[int data]\n902\n"),
		"n_quest/history.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n903\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}

	record := dnfrepo.QuestRecord{
		States: map[int64]dnfrepo.QuestState{
			10:  {Status: "accepted"},
			20:  {Status: "completed"},
			30:  {Status: "completed"},
			40:  {Status: "active"},
			70:  {Status: "completed"},
			674: {Status: "completed"},
		},
		Progress: map[int64]dnfrepo.QuestState{
			70: {Status: "in_progress"},
			90: {Status: "progress"},
		},
	}
	for id := int64(1000); id < 1674; id++ {
		record.States[id] = dnfrepo.QuestState{Status: "completed"}
	}
	before := dnfrepo.CloneQuest(record)

	got := catalog.HuntPlanningDependencyIDs(record)
	want := []int64{20, 30, 40, 50, 60, 80}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies=%v, want %v", got, want)
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatalf("record mutated: got=%+v, want=%+v", record, before)
	}
}
