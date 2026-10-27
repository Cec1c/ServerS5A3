package quest

import (
	"context"
	"reflect"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerAutoCompleteMainClosesSelectedActiveEpicAndItsLinkedSubQuestsWithoutRewards(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:1",
		Job:         "2",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 0, "sp": 777, "exp": 12345},
	}
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "17",
		Slots: map[string]dnfrepo.ItemStack{
			"0:10": {ItemID: 9001, Count: 3},
		},
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	skill := dnfrepo.SkillRecord{CharacterID: "17", Skills: map[int64]dnfrepo.SkillState{1001: {Level: 5, Enabled: true}}}
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 1},
			102: {Status: "active", ProgressValue: 1},
			103: {Status: "active", ProgressValue: 1},
			106: {Status: "active", ProgressValue: 1},
		},
		Progress: map[int64]dnfrepo.QuestState{
			150: {Status: "active", ProgressValue: 3},
		},
	}); err != nil {
		t.Fatal(err)
	}

	catalog := autoCompleteMainTestCatalog(t)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, 7, 22, 7, 30, 0, 0, time.UTC)
	result, err := owner.ApplyAutoCompleteMain(ctx, catalog, AutoCompleteMainInput{
		AccountID:     "dnf:1",
		CharacterID:   "17",
		Eligibility:   CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		CutoffLevel:   89,
		TargetQuestID: 100,
		CompletedAt:   completedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.ChangedQuestIDs, []int64{100}) ||
		!reflect.DeepEqual(result.ChangedLinkedSubIDs, []int64{150}) ||
		result.ActiveCompletedCount != 1 || result.HighestCompletedQuest != 100 || result.Idempotent {
		t.Fatalf("auto-complete result=%+v", result)
	}
	stored := result.PostCommitQuest
	for _, questID := range []int64{100} {
		state := stored.States[questID]
		if state.Status != "completed" || state.ProgressValue != 0 ||
			state.Extra["reward_state"] != finishRewardGranted ||
			state.Extra["auto_complete_reason"] != "epic_below_character_level_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "89" {
			t.Fatalf("completed epic quest %d state=%+v", questID, state)
		}
	}
	if _, exists := stored.States[101]; exists {
		t.Fatalf("absent successor quest 101 was manufactured: %+v", stored.States[101])
	}
	if acceptable := catalog.Acceptable(CharacterEligibility{Level: 90, Job: 2, GrowType: 0}, stored); !containsQuestID(acceptable.IDs, 101) {
		t.Fatalf("successor main quest 101 missing after active-only completion: %v", acceptable.IDs)
	}
	// `[sub]` 闭包：150 的 `[main quest]` 是 100，100 被完成 ⇒ 150 一并完成，
	// 否则客户端会保留一个"父已完成、子仍可接取"的死节点。
	if state := stored.Progress[150]; state.Status != "completed" || state.ProgressValue != 0 ||
		state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
		state.Extra["main_quest_id"] != "100" || state.Extra["reward_state"] != finishRewardGranted {
		t.Fatalf("linked sub state=%+v", state)
	}
	for _, questID := range []int64{102, 103, 106} {
		if state := stored.States[questID]; state.Status != "active" || state.ProgressValue != 1 {
			t.Fatalf("non-target quest %d changed: %+v", questID, state)
		}
	}
	for _, questID := range []int64{104, 105} {
		if _, exists := stored.States[questID]; exists {
			t.Fatalf("inapplicable/answer quest %d was manufactured", questID)
		}
	}

	postCharacter, found, err := repositories.Character.Load(ctx, "17")
	if err != nil || !found || !reflect.DeepEqual(postCharacter, character) {
		t.Fatalf("character changed found=%t err=%v got=%+v want=%+v", found, err, postCharacter, character)
	}
	postInventory, found, err := repositories.Inventory.Load(ctx, "17")
	if err != nil || !found || !reflect.DeepEqual(postInventory, inventory) {
		t.Fatalf("inventory changed found=%t err=%v got=%+v want=%+v", found, err, postInventory, inventory)
	}
	postSkill, found, err := repositories.Skill.Load(ctx, "17")
	if err != nil || !found || !reflect.DeepEqual(postSkill, skill) {
		t.Fatalf("skill changed found=%t err=%v got=%+v want=%+v", found, err, postSkill, skill)
	}

	replayed, err := owner.ApplyAutoCompleteMain(ctx, catalog, AutoCompleteMainInput{
		AccountID:     "dnf:1",
		CharacterID:   "17",
		Eligibility:   CharacterEligibility{Level: 90, Job: 2, GrowType: 0},
		CutoffLevel:   89,
		TargetQuestID: 100,
		CompletedAt:   completedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Idempotent || len(replayed.ChangedQuestIDs) != 0 || len(replayed.ChangedLinkedSubIDs) != 0 {
		t.Fatalf("idempotent replay=%+v", replayed)
	}
}

// TestPlanAutoCompleteMainZeroTargetFollowsTheConfiguredScope 固定任务手册
// "任务完成"(op1429 零 selector) 的批量口径由 scope 决定：
//
//   - MainStorySweep（线上默认，与客户端确认框"自动完成 Lv89 为止的主线任务"一致）
//     ⇒ 100/106（激活）与 101 合法完成；105 缺少真实问答前置而保留；102 声明 [level] 90
//     超出 89 上限，103 是支线，104 职业不符，150 是 [sub] —— 都不动。
//   - ActiveOnly（DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP=0 的回退口径）
//     ⇒ 只完成 100/106，绝不制造没接过的行。
//
// 两种 scope 都不发奖励：只写 Status/ProgressValue/Extra。
func TestPlanAutoCompleteMainZeroTargetFollowsTheConfiguredScope(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	clearedAt := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	newRecord := func() dnfrepo.QuestRecord {
		return dnfrepo.QuestRecord{
			CharacterID: "17",
			States: map[int64]dnfrepo.QuestState{
				100: {Status: "active", ProgressValue: 1},
				106: {Status: "active", ProgressValue: 1},
			},
		}
	}

	record := newRecord()
	plan, err := catalog.PlanAutoCompleteMainQuests(
		record,
		CharacterEligibility{Level: 90, Job: 2},
		89,
		0,
		CouponClearScopeMainStorySweep,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := plan.Record.States[105]; exists {
		t.Fatal("unanswered branch completed")
	}
	if !reflect.DeepEqual(plan.EligibleQuestIDs, []int64{100, 101, 106}) ||
		!reflect.DeepEqual(plan.ChangedQuestIDs, []int64{100, 101, 106}) ||
		plan.ActiveCompletedCount != 2 || plan.HighestCompletedQuest != 106 {
		t.Fatalf("sweep zero-selector plan=%+v", plan)
	}
	for _, questID := range []int64{100, 101, 106} {
		state := plan.Record.States[questID]
		if state.Status != "completed" || state.ProgressValue != 0 ||
			state.Extra["reward_state"] != finishRewardGranted ||
			state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "89" {
			t.Fatalf("sweep-completed quest %d state=%+v", questID, state)
		}
	}
	if record.States[100].Status != "active" || record.States[106].Status != "active" {
		t.Fatalf("planning mutated source record=%+v", record)
	}

	activeOnly, err := catalog.PlanAutoCompleteMainQuests(
		newRecord(),
		CharacterEligibility{Level: 90, Job: 2},
		89,
		0,
		CouponClearScopeActiveOnly,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(activeOnly.EligibleQuestIDs, []int64{100, 106}) ||
		!reflect.DeepEqual(activeOnly.ChangedQuestIDs, []int64{100, 106}) ||
		activeOnly.ActiveCompletedCount != 2 || activeOnly.HighestCompletedQuest != 106 {
		t.Fatalf("active-only zero-selector plan=%+v", activeOnly)
	}
	if _, exists := activeOnly.Record.States[101]; exists {
		t.Fatalf("active-only manufactured quest 101: %+v", activeOnly.Record.States[101])
	}
}

// TestPlanAutoCompleteMainZeroTargetWithNothingLeftIsAnEmptyPlan 固定"整段主线
// 都已经完成（连 `[sub]` 闭包也满足）"时的批量动作是幂等空操作（返回空计划、不报错），
// 这样 op1429 才能像现场第二次点击那样回 committed=0，而不是把整条请求判失败。
func TestPlanAutoCompleteMainZeroTargetWithNothingLeftIsAnEmptyPlan(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	completed := dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed"},
			101: {Status: "completed"},
			102: {Status: "active", ProgressValue: 1},
			103: {Status: "active", ProgressValue: 1},
			105: {Status: "completed"},
			106: {Status: "completed"},
			// 150 是 100 的 `[sub]`；它没完成时 sweep 会把它补完（见
			// TestPlanCouponMainQuestClearRepairsADanglingLinkedSubQuest），
			// 所以"真的一件都不剩"必须把它也算上。
			150: {Status: "completed"},
		},
	}
	for _, scope := range []CouponClearScope{CouponClearScopeMainStorySweep, CouponClearScopeActiveOnly} {
		plan, err := catalog.PlanAutoCompleteMainQuests(
			completed,
			CharacterEligibility{Level: 90, Job: 2},
			89,
			0,
			scope,
			time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC),
		)
		if err != nil {
			t.Fatalf("scope=%s error=%v", scope, err)
		}
		if len(plan.ChangedFields) != 0 || len(plan.ChangedQuestIDs) != 0 {
			t.Fatalf("scope=%s plan is not empty: %+v", scope, plan)
		}
		if state := plan.Record.States[102]; state.Status != "active" {
			t.Fatalf("scope=%s touched the out-of-range epic: %+v", scope, state)
		}
	}
}

// TestPlanAutoCompleteMainExactTargetStillCompletesOnlyThatQuest 固定非零 selector
// 的形状没有随口径一起改变：UI 上指定一行时仍然只完成那一行。
func TestPlanAutoCompleteMainExactTargetStillCompletesOnlyThatQuest(t *testing.T) {
	catalog := autoCompleteMainTestCatalog(t)
	plan, err := catalog.PlanAutoCompleteMainQuests(
		dnfrepo.QuestRecord{
			CharacterID: "17",
			States: map[int64]dnfrepo.QuestState{
				100: {Status: "active", ProgressValue: 1},
				106: {Status: "active", ProgressValue: 1},
			},
		},
		CharacterEligibility{Level: 90, Job: 2},
		89,
		106,
		CouponClearScopeMainStorySweep,
		time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.ChangedQuestIDs, []int64{106}) ||
		plan.Record.States[100].Status != "active" {
		t.Fatalf("exact-target plan=%+v", plan)
	}
	for _, questID := range []int64{101, 105} {
		if _, exists := plan.Record.States[questID]; exists {
			t.Fatalf("exact target manufactured quest %d: %+v", questID, plan.Record.States[questID])
		}
	}
}

func autoCompleteMainTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList:                     "100 `epic_start.qst`\n101 `epic_next.qst`\n102 `epic_level_90.qst`\n103 `side.qst`\n104 `other_job.qst`\n105 `answer.qst`\n106 `other_active_epic.qst`\n150 `linked_sub.qst`\n",
		"n_quest/epic_start.qst":        questCatalogTestDefinition("[epic]", 20, 99, "[gunner]", "[type]\n`[clear map]`\n"),
		"n_quest/epic_next.qst":         questCatalogTestDefinition("[epic]", 30, 99, "[gunner]", "[type]\n`[meet npc]`\n[pre required quest]\n100\n"),
		"n_quest/epic_level_90.qst":     questCatalogTestDefinition("[epic]", 90, 99, "[gunner]", "[type]\n`[meet npc]`\n[pre required quest]\n101\n"),
		"n_quest/side.qst":              questCatalogTestDefinition("[side]", 10, 99, "[gunner]", "[type]\n`[meet npc]`\n"),
		"n_quest/other_job.qst":         questCatalogTestDefinition("[epic]", 10, 99, "[fighter]", "[type]\n`[meet npc]`\n"),
		"n_quest/answer.qst":            questCatalogTestDefinition("[epic]", 40, 99, "[gunner]", "[type]\n`[meet npc]`\n[pre required quest answer]\n1\n"),
		"n_quest/other_active_epic.qst": questCatalogTestDefinition("[epic]", 35, 99, "[gunner]", "[type]\n`[meet npc]`\n"),
		"n_quest/linked_sub.qst":        questCatalogTestDefinition("[sub]", 30, 99, "[gunner]", "[type]\n`[hunt enemy]`\n[main quest]\n100\n"),
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
