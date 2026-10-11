package quest

import (
	"context"
	"errors"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The fixtures below reproduce the live 90CN shape (character 3, job 14,
// grow_type 0x21, level 90) that produced
// "点击可接取但不能接取":
//
//	3971 [epic] [quest clear] "离别时刻" level 88..99, [pre required quest] 3970,
//	     [int data] 3972 3973
//	3972 [sub] [main quest] 3971
//	3973 [sub] [main quest] 3971
//	3974 [epic] [pre required quest] 3971   (the chain successor)
//
// The Lv1~90 main-story completion coupon marks [epic] quests at or below the
// cutoff completed, so it completed 3971 while its grade-[sub] children stayed
// outside the sweep. Listing those children made the current client render a
// task-manual node for main quest 3971 and answer the click with op31(3971),
// which the accept preflight refuses with ErrQuestNotAcceptable.
func farewellMomentQuestListSource() catalogTestSource {
	return catalogTestSource{
		DefaultList:           "3970 `beast24.qst`\n3971 `beast25.qst`\n3972 `beast25_01.qst`\n3973 `beast25_02.qst`\n3974 `fear01.qst`\n",
		"n_quest/beast24.qst": questCatalogTestDefinition("[epic]", 88, 99, "[all]", ""),
		"n_quest/beast25.qst": questCatalogTestDefinition("[epic]", 88, 99, "[all]",
			"[pre required quest]\n3970\n[type]\n`[quest clear]`\n[int data]\n3972 3973\n"),
		"n_quest/beast25_01.qst": questCatalogTestDefinition("[sub]", 88, 99, "[all]",
			"[main quest]\n3971\n[type]\n`[meet npc]`\n[int data]\n455\n"),
		"n_quest/beast25_02.qst": questCatalogTestDefinition("[sub]", 88, 99, "[all]",
			"[main quest]\n3971\n[type]\n`[clear map]`\n[int data]\n76781\n"),
		"n_quest/fear01.qst": questCatalogTestDefinition("[epic]", 89, 99, "[all]",
			"[pre required quest]\n3971\n[type]\n`[clear map]`\n[int data]\n76909\n"),
	}
}

// boundaryQuestListSource holds the [main quest] boundaries the client list
// must respect: an out-of-level-range parent, a job-restricted parent, and a
// repeatable (daily) parent that stays acceptable after completion.
func boundaryQuestListSource() catalogTestSource {
	return catalogTestSource{
		DefaultList:        "5000 `late.qst`\n5001 `late_01.qst`\n5010 `fighter_only.qst`\n5011 `fighter_only_01.qst`\n5020 `daily.qst`\n5021 `daily_01.qst`\n",
		"n_quest/late.qst": questCatalogTestDefinition("[epic]", 95, 99, "[all]", ""),
		"n_quest/late_01.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n5000\n[type]\n`[meet npc]`\n[int data]\n455\n"),
		"n_quest/fighter_only.qst": questCatalogTestDefinition("[epic]", 1, 99, "[fighter]", ""),
		"n_quest/fighter_only_01.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n5010\n[type]\n`[meet npc]`\n[int data]\n455\n"),
		"n_quest/daily.qst": questCatalogTestDefinition("[daily]", 1, 99, "[all]", ""),
		"n_quest/daily_01.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n5020\n[type]\n`[meet npc]`\n[int data]\n455\n"),
	}
}

func loadQuestCatalogForTest(t *testing.T, source catalogTestSource) *Catalog {
	t.Helper()
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

func questListIDs(t *testing.T, catalog *Catalog, eligibility CharacterEligibility, record dnfrepo.QuestRecord) []int32 {
	t.Helper()
	return catalog.QuestList(eligibility, record).IDs
}

// assertClientListSatisfiesAcceptPreflight is the invariant this file exists
// for: every quest the client is offered survives the accept preflight, and so
// does every [main quest] ancestor the client can resolve that row to.
func assertClientListSatisfiesAcceptPreflight(t *testing.T, catalog *Catalog, eligibility CharacterEligibility, record dnfrepo.QuestRecord) {
	t.Helper()
	for _, questID := range questListIDs(t, catalog, eligibility, record) {
		if decision, err := catalog.DecideAccept(eligibility, record, int64(questID)); decision == AcceptRejected {
			t.Fatalf("listed quest %d is refused by the accept preflight: %v", questID, err)
		}
		definition, ok := catalog.Find(int64(questID))
		if !ok {
			t.Fatalf("listed quest %d has no PVF definition", questID)
		}
		for ancestorID := definition.MainQuestID; ancestorID > 0; {
			ancestor, ok := catalog.Find(ancestorID)
			if !ok {
				break
			}
			if decision, err := catalog.DecideAccept(eligibility, record, ancestorID); decision == AcceptRejected {
				t.Fatalf("listed quest %d resolves to [main quest] %d, which the accept preflight refuses: %v", questID, ancestorID, err)
			}
			if ancestor.MainQuestID == ancestorID {
				break
			}
			ancestorID = ancestor.MainQuestID
		}
	}
}

func TestClientAcceptableListDropsSubQuestsOfACompletedMainQuest(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, farewellMomentQuestListSource())
	eligibility := CharacterEligibility{Level: 90, Job: 14, GrowType: 0x21}
	// Live post-sweep record: the completion coupon marked the [epic] [quest
	// clear] main quest 3971 completed; the [sub] children 3972/3973 carry
	// grade [sub] and were never swept, so they used to stay in the op21 list.
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
		3971: {Status: "completed"},
	}}
	list := catalog.QuestList(eligibility, record)
	for _, unwanted := range []int32{3971, 3972, 3973} {
		if containsQuestID(list.IDs, unwanted) {
			t.Fatalf("quest %d is still offered to the client: ids=%v", unwanted, list.IDs)
		}
	}
	if !containsQuestID(list.IDs, 3974) {
		t.Fatalf("the epic successor 3974 must stay acceptable: ids=%v", list.IDs)
	}
	if list.PreflightExcluded != 2 {
		t.Fatalf("preflight-excluded=%d want 2 (the two [sub] rows of the completed main quest) ids=%v", list.PreflightExcluded, list.IDs)
	}
	decision, err := catalog.DecideAccept(eligibility, record, 3971)
	if decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("completed main quest 3971 decision=%v err=%v, want rejected/ErrQuestNotAcceptable", decision, err)
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 3974); decision != AcceptNew || err != nil {
		t.Fatalf("successor 3974 decision=%v err=%v, want AcceptNew", decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

func TestClientAcceptableListKeepsSubQuestsWhileTheMainQuestIsAcceptable(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, farewellMomentQuestListSource())
	eligibility := CharacterEligibility{Level: 90, Job: 14, GrowType: 0x21}
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
	}}
	list := catalog.QuestList(eligibility, record)
	for _, wanted := range []int32{3971, 3972, 3973} {
		if !containsQuestID(list.IDs, wanted) {
			t.Fatalf("quest %d must stay listed while its main quest is acceptable: ids=%v", wanted, list.IDs)
		}
	}
	if containsQuestID(list.IDs, 3974) {
		t.Fatalf("successor 3974 must wait for 3971: ids=%v", list.IDs)
	}
	if list.PreflightExcluded != 0 {
		t.Fatalf("preflight-excluded=%d want 0: %v", list.PreflightExcluded, list.IDs)
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 3971); decision != AcceptNew || err != nil {
		t.Fatalf("pending main quest 3971 decision=%v err=%v, want AcceptNew", decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

func TestClientAcceptableListKeepsSubQuestsWhileTheMainQuestIsActive(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, farewellMomentQuestListSource())
	eligibility := CharacterEligibility{Level: 90, Job: 14, GrowType: 0x21}
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
		3971: {Status: "active", ProgressValue: 2},
	}}
	list := catalog.QuestList(eligibility, record)
	for _, wanted := range []int32{3971, 3972, 3973} {
		if !containsQuestID(list.IDs, wanted) {
			t.Fatalf("quest %d must stay listed while its main quest is active: ids=%v", wanted, list.IDs)
		}
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 3971); decision != AcceptIdempotent || err != nil {
		t.Fatalf("active main quest 3971 decision=%v err=%v, want AcceptIdempotent", decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

func TestClientAcceptableListRespectsMainQuestLevelAndJobBoundaries(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, boundaryQuestListSource())

	// Level 90: the level 95..99 parent is refused, so its [sub] child must not
	// be offered even though the child's own [level] window would allow it.
	level90 := CharacterEligibility{Level: 90, Job: 2, GrowType: 0}
	list := questListIDs(t, catalog, level90, dnfrepo.QuestRecord{})
	if containsQuestID(list, 5001) {
		t.Fatalf("sub quest 5001 was offered while its main quest 5000 is out of level range: ids=%v", list)
	}
	// Level 95: the parent becomes acceptable, so the child returns.
	level95 := CharacterEligibility{Level: 95, Job: 2, GrowType: 0}
	list = questListIDs(t, catalog, level95, dnfrepo.QuestRecord{})
	for _, wanted := range []int32{5000, 5001} {
		if !containsQuestID(list, wanted) {
			t.Fatalf("quest %d must be offered once the main quest is in level range: ids=%v", wanted, list)
		}
	}

	// Job boundary: the parent is [fighter]; a gunner must not be offered the
	// child that resolves to it, while a fighter must.
	gunner := questListIDs(t, catalog, level90, dnfrepo.QuestRecord{})
	if containsQuestID(gunner, 5011) || containsQuestID(gunner, 5010) {
		t.Fatalf("job-restricted main quest rows leaked into the gunner list: ids=%v", gunner)
	}
	fighter := questListIDs(t, catalog, CharacterEligibility{Level: 90, Job: 1, GrowType: 0}, dnfrepo.QuestRecord{})
	for _, wanted := range []int32{5010, 5011} {
		if !containsQuestID(fighter, wanted) {
			t.Fatalf("quest %d must be offered to its own job: ids=%v", wanted, fighter)
		}
	}

	assertClientListSatisfiesAcceptPreflight(t, catalog, level90, dnfrepo.QuestRecord{})
	assertClientListSatisfiesAcceptPreflight(t, catalog, level95, dnfrepo.QuestRecord{})
	assertClientListSatisfiesAcceptPreflight(t, catalog, CharacterEligibility{Level: 90, Job: 1, GrowType: 0}, dnfrepo.QuestRecord{})
}

func TestClientAcceptableListKeepsSubQuestsOfARepeatableCompletedMainQuest(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, boundaryQuestListSource())
	eligibility := CharacterEligibility{Level: 90, Job: 2, GrowType: 0}
	record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		5020: {Status: "completed"},
	}}
	list := questListIDs(t, catalog, eligibility, record)
	for _, wanted := range []int32{5020, 5021} {
		if !containsQuestID(list, wanted) {
			t.Fatalf("repeatable main quest %d and its sub must stay listed: ids=%v", wanted, list)
		}
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 5020); decision != AcceptNew || err != nil {
		t.Fatalf("repeatable completed main quest 5020 decision=%v err=%v, want AcceptNew", decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

func TestClientAcceptableListInvariantHoldsForEveryRecordShape(t *testing.T) {
	catalog := loadQuestCatalogForTest(t, farewellMomentQuestListSource())
	records := map[string]dnfrepo.QuestRecord{
		"empty": {},
		"prerequisite-completed": {States: map[int64]dnfrepo.QuestState{
			3970: {Status: "completed"},
		}},
		"main-completed": {States: map[int64]dnfrepo.QuestState{
			3970: {Status: "completed"},
			3971: {Status: "completed"},
		}},
		"main-active": {States: map[int64]dnfrepo.QuestState{
			3970: {Status: "completed"},
			3971: {Status: "active"},
		}},
		"sub-active-under-completed-main": {States: map[int64]dnfrepo.QuestState{
			3970: {Status: "completed"},
			3971: {Status: "completed"},
			3972: {Status: "active"},
		}},
		"legacy-progress-only": {Progress: map[int64]dnfrepo.QuestState{
			3970: {Status: "completed"},
			3971: {Status: "completed"},
		}},
	}
	eligibilities := []CharacterEligibility{
		{Level: 90, Job: 14, GrowType: 0x21},
		{Level: 1, Job: 14, GrowType: 0},
		{Level: 95, Job: 2, GrowType: 0x12},
	}
	for name, record := range records {
		for _, eligibility := range eligibilities {
			assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
			list := catalog.QuestList(eligibility, record)
			for _, questID := range list.IDs {
				definition, ok := catalog.Find(int64(questID))
				if !ok {
					t.Fatalf("%s: listed quest %d has no definition", name, questID)
				}
				if eligibility.Level < definition.LevelMin || eligibility.Level > definition.LevelMax {
					t.Fatalf("%s: listed quest %d is outside its level window %d..%d for level %d",
						name, questID, definition.LevelMin, definition.LevelMax, eligibility.Level)
				}
				if !definitionMatchesCharacter(definition, eligibility) {
					t.Fatalf("%s: listed quest %d does not match job %d grow %#x", name, questID, eligibility.Job, eligibility.GrowType)
				}
			}
		}
	}
}
