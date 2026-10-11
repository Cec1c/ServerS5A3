package quest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The runtime Script.pvf cases below pin the invariant that the live 90CN
// defect broke: whatever the op21 acceptable-quest list offers, the op31 accept
// preflight must accept (or idempotently acknowledge) for the quest itself and
// for every [main quest] node the client can resolve the row to.

func loadRealScriptPVFCatalogForClientList(t *testing.T) (*Catalog, *dnfpvf.Index) {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the client acceptable-quest list against the runtime PVF")
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
	return catalog, index
}

// farewellMomentEligibility is live character 3 (test2): job 14, grow_type 0x21
// (second awakening), level 90.
func farewellMomentEligibility() CharacterEligibility {
	return CharacterEligibility{Level: 90, Job: 14, GrowType: 0x21}
}

// TestRealScriptPVFQuest3971IsTheFarewellMomentMainQuest identifies the quest
// from the operator's screenshot («主线 离别时刻 / 点击可接取») and records the
// PVF facts the fix depends on.
func TestRealScriptPVFQuest3971IsTheFarewellMomentMainQuest(t *testing.T) {
	catalog, index := loadRealScriptPVFCatalogForClientList(t)
	definition, ok := catalog.Find(3971)
	if !ok {
		t.Fatal("real PVF quest 3971 is missing")
	}
	wantPath := "n_quest/quest_renewal/epic_quest/devildom/timebeast/timebeast_epic_25.qst"
	if definition.Path != wantPath ||
		normalizeQuestTag(definition.Grade) != "epic" ||
		normalizeQuestTag(definition.Type) != "quest clear" ||
		definition.LevelMin != 88 || definition.LevelMax != 99 ||
		definition.MainQuestID != 0 ||
		definition.HasDependGiveItem ||
		len(definition.PreRequiredGroups) != 1 ||
		len(definition.PreRequiredGroups[0]) != 1 ||
		definition.PreRequiredGroups[0][0] != 3970 ||
		len(definition.IntData) != 2 || definition.IntData[0] != 3972 || definition.IntData[1] != 3973 {
		t.Fatalf("quest 3971 definition=%+v, want %s [epic] [quest clear] 88..99 pre=[[3970]] int=[3972 3973]", definition, wantPath)
	}
	// The client shows this quest name in the right-hand "new main quest" hint,
	// so the [name] token is the link back to the operator report.
	document, ok := index.Document(wantPath)
	if !ok {
		t.Fatalf("quest 3971 document %s is not indexed", wantPath)
	}
	name, ok := document.Text("name")
	if !ok || strings.TrimSpace(name) != "离别时刻" {
		t.Fatalf("quest 3971 [name]=%q found=%t, want 离别时刻", name, ok)
	}
	for _, childID := range []int64{3972, 3973} {
		child, ok := catalog.Find(childID)
		if !ok {
			t.Fatalf("real PVF child quest %d is missing", childID)
		}
		if child.MainQuestID != 3971 || normalizeQuestTag(child.Grade) != "sub" {
			t.Fatalf("child quest %d definition=%+v, want [sub] with [main quest] 3971", childID, child)
		}
	}
	successor, ok := catalog.Find(3974)
	if !ok || len(successor.PreRequiredGroups) != 1 || successor.PreRequiredGroups[0][0] != 3971 {
		t.Fatalf("quest 3974 definition=%+v, want [pre required quest] 3971", successor)
	}
}

// TestRealScriptPVFCompletedFarewellMomentHidesItsSubQuestsAndKeepsTheSuccessor
// is the regression for the reported symptom: 3971 is completed, so neither it
// nor its [sub] children may be offered, while the epic successor 3974 must
// stay acceptable (the fix must not over-prune).
func TestRealScriptPVFCompletedFarewellMomentHidesItsSubQuestsAndKeepsTheSuccessor(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
		3971: {Status: "completed"},
	}}
	list := catalog.QuestList(eligibility, record)
	for _, unwanted := range []int32{3971, 3972, 3973} {
		if containsQuestID(list.IDs, unwanted) {
			t.Fatalf("quest %d is still offered as acceptable: ids=%v", unwanted, list.IDs)
		}
	}
	if !containsQuestID(list.IDs, 3974) {
		t.Fatalf("epic successor 3974 must stay acceptable: ids=%v", list.IDs)
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 3971); decision != AcceptRejected || !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("completed main quest 3971 decision=%v err=%v, want rejected/ErrQuestNotAcceptable", decision, err)
	}
	if decision, err := catalog.DecideAccept(eligibility, record, 3974); decision != AcceptNew || err != nil {
		t.Fatalf("successor 3974 decision=%v err=%v, want AcceptNew", decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

// TestRealScriptPVFMainStoryCouponSweepKeepsTheClientListAcceptable replays the
// live defect end to end with the real catalog: the Lv1~90 coupon completes
// [epic] quests up to Lv89 - including the [quest clear] parent 3971 - and
// leaves its grade-[sub] children alone. The resulting op21 list must still be
// fully acceptable.
func TestRealScriptPVFMainStoryCouponSweepKeepsTheClientListAcceptable(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	cutoff := CouponClearCutoff(eligibility.Level)
	if cutoff != CouponMainQuestClearMaxLevel {
		t.Fatalf("cutoff=%d want %d for level %d", cutoff, CouponMainQuestClearMaxLevel, eligibility.Level)
	}
	plan, err := catalog.PlanCouponMainQuestClear(
		dnfrepo.QuestRecord{CharacterID: "3"},
		eligibility,
		cutoff,
		CouponClearScopeMainStorySweep,
		time.Date(2026, 9, 25, 4, 39, 33, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	record := plan.Record
	if state, ok := record.States[3971]; !ok || !isCompletedQuestStatus(state.Status) {
		t.Fatalf("the sweep must complete the level-88 main quest 3971: %+v", state)
	}
	// 决策 1：券把被扫中父任务的 `[sub]` 子任务一并完成，所以 3972/3973 在扫完
	// 之后必须是 completed——"父已完成、子仍可接取"从源头不再产生。
	for _, childID := range []int64{3972, 3973} {
		state, ok := record.States[childID]
		if !ok || !isCompletedQuestStatus(state.Status) {
			t.Fatalf("the sweep must complete the [sub] child %d together with its main quest: %+v", childID, state)
		}
		if state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
			state.Extra["main_quest_id"] != "3971" {
			t.Fatalf("child %d extra=%+v", childID, state.Extra)
		}
	}
	list := catalog.QuestList(eligibility, record)
	if len(list.IDs) == 0 {
		t.Fatal("the swept record must still have an acceptable-quest list")
	}
	// 源头上已经修好：扫完的 `[sub]` 子任务都完成了，所以 accept-preflight 过滤器
	// 在这一形态下**无事可做**。（过滤器仍然有效——旧记录的自愈形态由
	// TestRealScriptPVFCouponSweepRepairsTheLiveDanglingFarewellMomentState 断言
	// preflight_excluded 2 -> 0。两者是"源头修好"与"安全网兜住"两个方向。）
	if list.PreflightExcluded != 0 {
		t.Fatalf("a completed sweep must leave nothing for the safety net: excluded=%d ids=%v", list.PreflightExcluded, list.IDs)
	}
	for _, unwanted := range []int32{3971, 3972, 3973} {
		if containsQuestID(list.IDs, unwanted) {
			t.Fatalf("quest %d is still offered after the sweep: ids=%v", unwanted, list.IDs)
		}
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}

// TestRealScriptPVFClientListNeverOffersOutOfRangeOrOtherJobQuests pins the
// negative half of the invariant on the real catalog: a quest that the level,
// job, or growth predicate excludes must not appear in the list at all.
func TestRealScriptPVFClientListNeverOffersOutOfRangeOrOtherJobQuests(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
		3971: {Status: "completed"},
	}}
	list := catalog.QuestList(eligibility, record)
	for _, questID := range list.IDs {
		definition, ok := catalog.Find(int64(questID))
		if !ok {
			t.Fatalf("listed quest %d has no PVF definition", questID)
		}
		if eligibility.Level < definition.LevelMin || eligibility.Level > definition.LevelMax {
			t.Fatalf("listed quest %d is outside its level window %d..%d for level %d",
				questID, definition.LevelMin, definition.LevelMax, eligibility.Level)
		}
		if !definitionMatchesCharacter(definition, eligibility) {
			t.Fatalf("listed quest %d job=%q target=%q does not match job %d grow %#x",
				questID, definition.Job, definition.TargetCharacter, eligibility.Job, eligibility.GrowType)
		}
	}

	// Explicit out-of-range example: the highest [level] floor in the catalog.
	highest := Definition{ID: -1}
	for _, definition := range catalog.ordered {
		if definition.LevelMin > highest.LevelMin {
			highest = definition
		}
	}
	if highest.ID <= 0 || highest.LevelMin <= eligibility.Level {
		t.Fatalf("catalog has no quest above level %d: %+v", eligibility.Level, highest)
	}
	if containsQuestID(list.IDs, int32(highest.ID)) {
		t.Fatalf("quest %d (level %d..%d) must not be offered to level %d", highest.ID, highest.LevelMin, highest.LevelMax, eligibility.Level)
	}

	// Explicit other-job example: an [epic] row restricted to a job that is not
	// the character's must be absent from this character's list.
	for _, definition := range catalog.ordered {
		if normalizeQuestTag(definition.Grade) != "epic" || definition.ExposedByNPC == 0 {
			continue
		}
		if definition.Job == "" || normalizeQuestTag(definition.Job) == "all" ||
			definitionMatchesCharacter(definition, eligibility) {
			continue
		}
		if containsQuestID(list.IDs, int32(definition.ID)) {
			t.Fatalf("job-restricted quest %d (%s) must not be offered to job %d", definition.ID, definition.Job, eligibility.Job)
		}
		assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
		return
	}
	t.Fatal("real catalog has no job-restricted [epic] quest to check the negative case with")
}

// TestRealScriptPVFActiveQuestStaysListedAndIsIdempotentlyAccepted keeps the
// documented "the All view also carries the active quest" contract while the
// preflight is unified: an active row stays listed because ApplyAccept
// acknowledges it instead of refusing it.
func TestRealScriptPVFActiveQuestStaysListedAndIsIdempotentlyAccepted(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	record := dnfrepo.QuestRecord{CharacterID: "3", States: map[int64]dnfrepo.QuestState{
		3970: {Status: "completed"},
		3971: {Status: "completed"},
	}}
	before := catalog.QuestList(eligibility, record)
	if len(before.IDs) == 0 {
		t.Fatal("no candidate quest to activate")
	}
	activeID := int64(before.IDs[0])
	record.States[activeID] = dnfrepo.QuestState{Status: "active", ProgressValue: 1}
	after := catalog.QuestList(eligibility, record)
	if !containsQuestID(after.IDs, int32(activeID)) {
		t.Fatalf("active quest %d disappeared from the list: ids=%v", activeID, after.IDs)
	}
	if decision, err := catalog.DecideAccept(eligibility, record, activeID); decision != AcceptIdempotent || err != nil {
		t.Fatalf("active quest %d decision=%v err=%v, want AcceptIdempotent", activeID, decision, err)
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, record)
}
