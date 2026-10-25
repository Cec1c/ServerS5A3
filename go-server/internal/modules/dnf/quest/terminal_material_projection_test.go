package quest

import (
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestProjectTerminalMaterialQuestClearProgressOnlyProjectsParentSnapshot(t *testing.T) {
	catalog, record := terminalMaterialProjectionFixture()
	before := dnfrepo.CloneQuest(record)

	projected := catalog.ProjectTerminalMaterialQuestClearProgress(record)

	if got := projected.States[100].ProgressValue; got != 0 {
		t.Fatalf("projected parent progress=%d want=0", got)
	}
	if got := projected.Progress[100].ProgressValue; got != 9 {
		t.Fatalf("shadowed legacy parent progress=%d want unchanged 9", got)
	}
	if got := projected.States[101].Status; got != "completed" {
		t.Fatalf("completed prior child status=%q want completed", got)
	}
	if got := projected.States[102]; got.Status != "active" || got.ProgressValue != 0 {
		t.Fatalf("terminal child state=%+v want active/0", got)
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatalf("input record mutated:\n got=%+v\nwant=%+v", record, before)
	}
	projected.States[100].Extra["projection_mutation"] = "detached"
	if _, exists := record.States[100].Extra["projection_mutation"]; exists {
		t.Fatal("projection shares nested Extra map with the input record")
	}
}

func TestProjectTerminalMaterialQuestClearProgressUsesProgressFallback(t *testing.T) {
	catalog, record := terminalMaterialProjectionFixture()
	record.Progress = record.States
	record.States = nil

	projected := catalog.ProjectTerminalMaterialQuestClearProgress(record)

	if got := projected.Progress[100].ProgressValue; got != 0 {
		t.Fatalf("projected legacy parent progress=%d want=0", got)
	}
	if got := record.Progress[100].ProgressValue; got != 1 {
		t.Fatalf("input legacy parent progress=%d want unchanged 1", got)
	}
}

func TestProjectTerminalMaterialQuestClearProgressAllowsSingleTerminalChild(t *testing.T) {
	catalog, record := terminalMaterialProjectionFixture()
	parent := catalog.byID[100]
	parent.IntData = []int64{102}
	catalog.byID[100] = parent
	delete(record.States, 101)

	projected := catalog.ProjectTerminalMaterialQuestClearProgress(record)

	if got := projected.States[100].ProgressValue; got != 0 {
		t.Fatalf("projected single-child parent progress=%d want=0", got)
	}
}

func TestProjectTerminalMaterialQuestClearProgressRejectsUnqualifiedStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Catalog, *dnfrepo.QuestRecord)
	}{
		{
			name: "prior child incomplete",
			mutate: func(_ *Catalog, record *dnfrepo.QuestRecord) {
				record.States[101] = dnfrepo.QuestState{Status: "active", ProgressValue: 0}
			},
		},
		{
			name: "terminal nonzero",
			mutate: func(_ *Catalog, record *dnfrepo.QuestRecord) {
				record.States[102] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
			},
		},
		{
			name: "terminal foreign parent",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				terminal := catalog.byID[102]
				terminal.MainQuestID = 999
				catalog.byID[102] = terminal
			},
		},
		{
			name: "terminal experience reward",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				terminal := catalog.byID[102]
				terminal.NoExperience = false
				catalog.byID[102] = terminal
			},
		},
		{
			name: "terminal independent reward",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				terminal := catalog.byID[102]
				terminal.RewardItems = []RewardItemRule{{ItemID: 200, Count: 1}}
				catalog.byID[102] = terminal
			},
		},
		{
			name: "terminal not material",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				terminal := catalog.byID[102]
				terminal.Type = "[hunt enemy]"
				catalog.byID[102] = terminal
			},
		},
		{
			name: "parent not quest clear",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				parent := catalog.byID[100]
				parent.Type = "[clear map]"
				catalog.byID[100] = parent
			},
		},
		{
			name: "parent absent from catalog",
			mutate: func(catalog *Catalog, _ *dnfrepo.QuestRecord) {
				delete(catalog.byID, 100)
			},
		},
		{
			name: "terminal absent from record",
			mutate: func(_ *Catalog, record *dnfrepo.QuestRecord) {
				delete(record.States, 102)
				delete(record.Progress, 102)
			},
		},
		{
			name: "states shadows parent with zero",
			mutate: func(_ *Catalog, record *dnfrepo.QuestRecord) {
				record.States[100] = dnfrepo.QuestState{Status: "active", ProgressValue: 0}
			},
		},
		{
			name: "states shadows parent with completed status",
			mutate: func(_ *Catalog, record *dnfrepo.QuestRecord) {
				record.States[100] = dnfrepo.QuestState{Status: "completed", ProgressValue: 1}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, record := terminalMaterialProjectionFixture()
			test.mutate(catalog, &record)
			before := dnfrepo.CloneQuest(record)
			projected := catalog.ProjectTerminalMaterialQuestClearProgress(record)
			if !reflect.DeepEqual(projected, before) {
				t.Fatalf("unexpected projection:\n got=%+v\nwant=%+v", projected, before)
			}
		})
	}
}

func TestProjectTerminalMaterialQuestClearProgressNilCatalogIsDetached(t *testing.T) {
	_, record := terminalMaterialProjectionFixture()
	before := dnfrepo.CloneQuest(record)
	projected := (*Catalog)(nil).ProjectTerminalMaterialQuestClearProgress(record)
	if !reflect.DeepEqual(projected, before) {
		t.Fatalf("projected=%+v want=%+v", projected, before)
	}
	projected.States[100].Extra["detached"] = "yes"
	if _, exists := record.States[100].Extra["detached"]; exists {
		t.Fatal("nil catalog projection shares input map")
	}
}

func terminalMaterialProjectionFixture() (*Catalog, dnfrepo.QuestRecord) {
	parent := Definition{
		ID: 100, Grade: "[epic]", Type: "[quest clear]", IntData: []int64{101, 102},
		NoExperience: true, RewardType: "[item]", RewardIntData: []int64{0, 0},
	}
	prior := Definition{
		ID: 101, MainQuestID: 100, Grade: "[sub]", Type: "[hunt enemy]",
		NoExperience: true, RewardType: "[item]", RewardIntData: []int64{0, 0},
	}
	terminal := Definition{
		ID: 102, MainQuestID: 100, Grade: "[sub]", Type: "[seeking]",
		IntData: []int64{10164764, 1}, NoExperience: true,
		RewardType: "[item]", RewardIntData: []int64{0, 0},
	}
	catalog := &Catalog{
		ordered: []Definition{parent, prior, terminal},
		byID: map[int64]Definition{
			100: parent,
			101: prior,
			102: terminal,
		},
	}
	record := dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 1, Extra: map[string]string{"source": "states"}},
			101: {Status: "completed", ProgressValue: 0},
			102: {Status: "active", ProgressValue: 0},
		},
		Progress: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 9},
			102: {Status: "active", ProgressValue: 5},
		},
	}
	return catalog, record
}
