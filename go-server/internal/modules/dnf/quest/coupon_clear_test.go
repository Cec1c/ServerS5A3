package quest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCouponClearCutoffCapsAtTheRealPVFMainStoryCeiling(t *testing.T) {
	cases := []struct {
		level int
		want  int
	}{
		{level: -5, want: 0},
		{level: 0, want: 0},
		{level: 1, want: 0},
		{level: 2, want: 2},
		{level: 40, want: 40},
		{level: 89, want: 89},
		{level: 90, want: 90},
		{level: 95, want: 90},
		{level: 999, want: 90},
	}
	for _, test := range cases {
		if got := CouponClearCutoff(test.level); got != test.want {
			t.Fatalf("CouponClearCutoff(%d)=%d want=%d", test.level, got, test.want)
		}
	}
	// 券的上限是券自己 PVF 说明里的 90（「完成Lv1至Lv90的主线任务」），不是
	// "等级-1"。任务手册"任务完成"按钮的那套 min(等级-1, 89) 口径留在
	// dnfbridge.currentAutoCompleteMainQuestMaxLevel，两边各自的文案不同。
	if CouponMainQuestClearMaxLevel != 90 {
		t.Fatalf("coupon ceiling changed to %d; re-derive it from the coupon PVF text before touching it", CouponMainQuestClearMaxLevel)
	}
}

func TestPlanCouponMainQuestClearSweepManufacturesOnlyNoRewardMainStoryRows(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 3, 38, 0, 0, time.UTC)
	plan, err := catalog.PlanCouponMainQuestClear(
		dnfrepo.QuestRecord{
			CharacterID: "17",
			States: map[int64]dnfrepo.QuestState{
				100: {Status: "active", ProgressValue: 5},
				106: {Status: "active", ProgressValue: 1},
			},
		},
		CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		89,
		CouponClearScopeMainStorySweep,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	got := append([]int64(nil), plan.ChangedQuestIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	// Missing or malformed question-answer prerequisites are a barrier, not
	// permission to complete every sibling. Independent 100/101/106 still clear.
	if want := []int64{100, 101, 106}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changed=%v want=%v", got, want)
	}
	if _, exists := plan.Record.States[105]; exists {
		t.Fatal("unanswered branch was manufactured")
	}
	for _, questID := range got {
		state := plan.Record.States[questID]
		if state.Status != "completed" || state.ProgressValue != 0 || !state.UpdatedAt.Equal(clearedAt) {
			t.Fatalf("quest %d state=%+v", questID, state)
		}
		if state.Extra["reward_state"] != finishRewardGranted ||
			state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "89" {
			t.Fatalf("quest %d extra=%+v", questID, state.Extra)
		}
	}
	if state := plan.Record.States[101]; state.Extra["pvf_path"] != "n_quest/epic_next.qst" ||
		state.Extra["quest_type"] != "[meet npc]" || len(state.Extra) != 6 {
		t.Fatalf("manufactured row must carry only PVF identity plus the coupon markers: %+v", state.Extra)
	}
	if plan.Record.States[102].Status != "" || plan.Record.States[103].Status != "" {
		t.Fatalf("out-of-scope rows were written: 102=%+v 103=%+v", plan.Record.States[102], plan.Record.States[103])
	}
	// `[sub]` 闭包：150 是 `[main quest] 100` 的 [sub]，100 被扫中 ⇒ 150 必须一并
	// 完成，否则客户端会保留一个"父已完成、子仍可接取"的死节点。
	if want := []int64{150}; !reflect.DeepEqual(plan.ChangedLinkedSubIDs, want) {
		t.Fatalf("linked sub closure=%v want=%v", plan.ChangedLinkedSubIDs, want)
	}
	subState := plan.Record.States[150]
	if subState.Status != "completed" || subState.ProgressValue != 0 || !subState.UpdatedAt.Equal(clearedAt) {
		t.Fatalf("linked sub state=%+v", subState)
	}
	if subState.Extra["reward_state"] != finishRewardGranted ||
		subState.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
		subState.Extra["main_quest_id"] != "100" ||
		subState.Extra["auto_complete_level_cutoff"] != "89" {
		t.Fatalf("linked sub extra=%+v", subState.Extra)
	}
	// 闭包行只写任务行字段，绝不带任何结算结构。
	for _, key := range []string{"settlement_receipt_v1", "settlement_source", "completion_key"} {
		if _, exists := subState.Extra[key]; exists {
			t.Fatalf("linked sub closure wrote a settlement key %q: %+v", key, subState.Extra)
		}
	}
}

func TestPlanCouponMainQuestClearActiveOnlyKeepsTheTaskManualScope(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 3, 38, 0, 0, time.UTC)
	plan, err := catalog.PlanCouponMainQuestClear(
		dnfrepo.QuestRecord{
			CharacterID: "17",
			States: map[int64]dnfrepo.QuestState{
				100: {Status: "active", ProgressValue: 5},
				106: {Status: "active", ProgressValue: 1},
			},
		},
		CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		89,
		CouponClearScopeActiveOnly,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	got := append([]int64(nil), plan.ChangedQuestIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if want := []int64{100, 106}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active-only changed quests=%v want=%v", got, want)
	}
	if _, exists := plan.Record.States[101]; exists {
		t.Fatalf("active-only manufactured quest 101: %+v", plan.Record.States[101])
	}
	// ActiveOnly 不制造没接过的主线行（101 不出现），但**本次真正完成的**父任务
	// 仍然要带上它的 `[sub]` 闭包：100 被这次调用完成 ⇒ 150 一并完成。
	// DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP=0 的回退语义是"范围变窄"，不是
	// "允许留下父已完成、子仍可接取的死节点"。
	if want := []int64{150}; !reflect.DeepEqual(plan.ChangedLinkedSubIDs, want) {
		t.Fatalf("active-only linked sub closure=%v want=%v", plan.ChangedLinkedSubIDs, want)
	}
	if state := plan.Record.States[150]; state.Status != "completed" ||
		state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" {
		t.Fatalf("active-only linked sub state=%+v", state)
	}
}

// TestPlanCouponMainQuestClearRepairsADanglingLinkedSubQuest 固定"自愈"口径：
// 一张被旧构建写成"父任务已完成、`[sub]` 子任务还挂着"的记录，在下一次 sweep
// 时会把子任务补完（父任务本身已经是 completed，不会重复计数），因此线上 char3
// 的 3971/3972/3973 不需要人工改库。
func TestPlanCouponMainQuestClearRepairsADanglingLinkedSubQuest(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 4, 39, 33, 0, time.UTC)
	dangling := dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed"},
			101: {Status: "completed"},
			// 102 的 [level] 是 90：券的上限就是 90，所以它同样在券的范围内
			// （这正是与"任务完成"按钮 min(等级-1, 89) 口径的差别）。
			102: {Status: "completed"},
			105: {Status: "completed"},
			106: {Status: "completed"},
		},
	}
	plan, err := catalog.PlanCouponMainQuestClear(
		dangling,
		CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		CouponClearMaxCutoffForTest(),
		CouponClearScopeMainStorySweep,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ChangedQuestIDs) != 0 {
		t.Fatalf("already-completed main quests were rewritten: %v", plan.ChangedQuestIDs)
	}
	if want := []int64{150}; !reflect.DeepEqual(plan.ChangedLinkedSubIDs, want) {
		t.Fatalf("repair closure=%v want=%v", plan.ChangedLinkedSubIDs, want)
	}
	if plan.Record.States[150].Status != "completed" || plan.HighestCompletedQuest != 150 {
		t.Fatalf("repaired record=%+v highest=%d", plan.Record.States[150], plan.HighestCompletedQuest)
	}
	// 自愈之后再规划一次必须是空计划（幂等）。
	replay, err := catalog.PlanCouponMainQuestClear(
		plan.Record,
		CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		CouponClearMaxCutoffForTest(),
		CouponClearScopeMainStorySweep,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.ChangedFields) != 0 {
		t.Fatalf("replay is not idempotent: %+v", replay)
	}
}

// CouponClearMaxCutoffForTest 是券在 Lv90 角色上的真实上限（PVF 说明里的 90）。
func CouponClearMaxCutoffForTest() int { return CouponClearCutoff(90) }

// TestCouponCeiling90IncludesLevel90MainQuestsWhileTheManualCeiling89DoesNot 固定
// 决策 2 的可观测差别：夹具里的 102 声明 `[level] 90`。券（上限 90）扫得到它，
// 任务手册"任务完成"按钮（上限 89）扫不到它——两条入口共用同一份 planner，
// 差别只在 cutoffLevel 参数。
func TestCouponCeiling90IncludesLevel90MainQuestsWhileTheManualCeiling89DoesNot(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 5, 45, 0, 0, time.UTC)
	empty := dnfrepo.QuestRecord{CharacterID: "17"}
	coupon, err := catalog.PlanCouponMainQuestClear(empty, CharacterEligibility{Level: 90, Job: 2}, CouponClearCutoff(90), CouponClearScopeMainStorySweep, clearedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt64(coupon.ChangedQuestIDs, 102) {
		t.Fatalf("coupon sweep at the PVF ceiling 90 must include the [level] 90 epic: %v", coupon.ChangedQuestIDs)
	}
	manual, err := catalog.PlanAutoCompleteMainQuests(empty, CharacterEligibility{Level: 90, Job: 2}, 89, 0, CouponClearScopeMainStorySweep, clearedAt)
	if err != nil {
		t.Fatal(err)
	}
	if containsInt64(manual.ChangedQuestIDs, 102) || manual.Record.States[102].Status != "" {
		t.Fatalf("manual sweep at 89 must not include the [level] 90 epic: %v", manual.ChangedQuestIDs)
	}
	for _, questID := range manual.ChangedQuestIDs {
		if !containsInt64(coupon.ChangedQuestIDs, questID) {
			t.Fatalf("quest %d is in the manual sweep but not in the coupon sweep", questID)
		}
	}
}

func TestPlanCouponMainQuestClearRejectsUnusableCutoffsAndDeferredInvalidStates(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 3, 38, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{100: {Status: "active"}}}
	for _, test := range []struct {
		name     string
		level    int
		cutoff   int
		cleared  time.Time
		expected error
	}{
		{name: "zero cutoff", level: 90, cutoff: 0, cleared: clearedAt, expected: ErrCouponClearLevelInvalid},
		// cutoff == 等级 是合法的：券的上限就是 90，而 Lv90 角色正好等于它。
		{name: "cutoff above level", level: 90, cutoff: 91, cleared: clearedAt, expected: ErrCouponClearLevelInvalid},
		{name: "zero cleared at", level: 90, cutoff: 89, expected: ErrCouponClearUnavailable},
	} {
		_, err := catalog.PlanCouponMainQuestClear(
			record,
			CharacterEligibility{Level: test.level, Job: 2},
			test.cutoff,
			CouponClearScopeMainStorySweep,
			test.cleared,
		)
		if !errors.Is(err, test.expected) {
			t.Fatalf("%s error=%v want=%v", test.name, err, test.expected)
		}
	}
	if _, err := (*Catalog)(nil).PlanCouponMainQuestClear(record, CharacterEligibility{Level: 90, Job: 2}, 89, CouponClearScopeMainStorySweep, clearedAt); !errors.Is(err, ErrCatalogEmpty) {
		t.Fatalf("nil catalog error=%v", err)
	}
}

func TestApplyCouponMainQuestClearIsIdempotentAndNeedsNoRewardTransaction(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "17", AccountID: "dnf:1", Job: "2", Level: 90,
		Stats: map[string]int64{"exp": 1, "gold": 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "17",
		States:      map[int64]dnfrepo.QuestState{100: {Status: "active"}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 9, 25, 3, 38, 0, 0, time.UTC)
	before, _, err := repositories.Character.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyCouponMainQuestClear(ctx, catalog, CouponClearInput{
		CharacterID: "17",
		Eligibility: CharacterEligibility{Level: 90, Job: 2},
		CutoffLevel: 89,
		ClearedAt:   clearedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Idempotent || first.Scope != CouponClearScopeMainStorySweep || first.CutoffLevel != 89 {
		t.Fatalf("first apply=%+v", first)
	}
	after, _, err := repositories.Character.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("coupon must not touch the character record: before=%+v after=%+v", before, after)
	}
	second, err := owner.ApplyCouponMainQuestClear(ctx, catalog, CouponClearInput{
		CharacterID: "17",
		Eligibility: CharacterEligibility{Level: 90, Job: 2},
		CutoffLevel: 89,
		ClearedAt:   clearedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Idempotent || len(second.ChangedQuestIDs) != 0 {
		t.Fatalf("replay must be idempotent: %+v", second)
	}
}
