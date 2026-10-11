package quest

import (
	"reflect"
	"sort"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The runtime Script.pvf cases below pin 决策 1（父任务被扫中 ⇒ `[sub]` 子任务一并
// 完成），即"父已完成、子仍挂在可接列表"从源头不再产生。判据本身
// （DecideAccept + `[main quest]` 祖先闭包）保留为不变式安全网，见
// client_acceptable_preflight_real_pvf_test.go。

// mainQuestForest walks every `[main quest]` edge in the real catalog and returns
// each child's parent plus the deepest chain length. It is the evidence behind
// mainQuestSweepMaxDepth and behind "闭包只沿 [main quest] 走，不会跨分支".
func mainQuestForest(catalog *Catalog) (parentOf map[int64]int64, maxDepth int, multiParent []int64) {
	parentOf = make(map[int64]int64)
	counts := make(map[int64]int)
	for _, definition := range catalog.ordered {
		if definition.MainQuestID <= 0 {
			continue
		}
		counts[definition.ID]++
		if counts[definition.ID] > 1 {
			multiParent = append(multiParent, definition.ID)
		}
		parentOf[definition.ID] = definition.MainQuestID
	}
	for _, definition := range catalog.ordered {
		depth := 0
		seen := map[int64]struct{}{definition.ID: {}}
		for current := definition.ID; ; {
			parent, ok := parentOf[current]
			if !ok {
				break
			}
			if _, cycle := seen[parent]; cycle {
				maxDepth = -1
				return parentOf, maxDepth, multiParent
			}
			seen[parent] = struct{}{}
			current = parent
			depth++
			if depth > 64 {
				maxDepth = -1
				return parentOf, maxDepth, multiParent
			}
		}
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	return parentOf, maxDepth, multiParent
}

// TestRealScriptPVFMainQuestForestIsAcyclicAndShallow is the termination
// evidence for the recursive `[sub]` closure: the `[main quest]` relation is a
// forest (one parent per quest), it has no cycle, and it is far shallower than
// mainQuestSweepMaxDepth. 因此闭包不会无限展开，也不会把别的分支的 [sub] 拖进来。
func TestRealScriptPVFMainQuestForestIsAcyclicAndShallow(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	parentOf, maxDepth, multiParent := mainQuestForest(catalog)
	if len(multiParent) != 0 {
		t.Fatalf("quests declared as a child more than once: %v", multiParent)
	}
	if maxDepth < 0 {
		t.Fatal("[main quest] relation has a cycle")
	}
	if maxDepth > mainQuestSweepMaxDepth {
		t.Fatalf("real [main quest] depth=%d exceeds mainQuestSweepMaxDepth=%d", maxDepth, mainQuestSweepMaxDepth)
	}
	subCount := 0
	for _, definition := range catalog.ordered {
		if normalizeQuestTag(definition.Grade) == "sub" && definition.MainQuestID > 0 {
			subCount++
		}
	}
	if subCount == 0 {
		t.Fatal("real catalog has no [sub] quest with a [main quest] parent")
	}
	t.Logf("[main quest] forest: edges=%d sub_edges=%d max_depth=%d", len(parentOf), subCount, maxDepth)
}

// TestRealScriptPVFCouponSweepCompletesEveryLinkedSubQuestOfASweptParent is
// 决策 1 的真档断言：用券（cutoff = 券说明里的 90）扫完之后，任何被扫中父任务的
// `[sub]` 子任务都不再处于"未完成"状态，因此也不可能再出现在可接列表里。
// 3971/3972/3973 是现场实例，其余父子对由真档全量枚举。
func TestRealScriptPVFCouponSweepCompletesEveryLinkedSubQuestOfASweptParent(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	cutoff := CouponClearCutoff(eligibility.Level)
	if CouponMainQuestClearMaxLevel != 90 || cutoff != 90 {
		t.Fatalf("coupon cutoff for level %d = %d (ceiling %d), want the PVF ceiling 90",
			eligibility.Level, cutoff, CouponMainQuestClearMaxLevel)
	}
	clearedAt := time.Date(2026, 9, 25, 4, 39, 33, 0, time.UTC)
	plan, err := catalog.PlanCouponMainQuestClear(
		dnfrepo.QuestRecord{CharacterID: "3"},
		eligibility,
		cutoff,
		CouponClearScopeMainStorySweep,
		clearedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	// 现场实例：3971 与它的两个 [sub] 子任务必须一起完成。
	for _, questID := range []int64{3971, 3972, 3973} {
		state, known := plan.Record.States[questID]
		if !known || !isCompletedQuestStatus(state.Status) {
			t.Fatalf("quest %d was not completed by the coupon sweep: %+v", questID, state)
		}
	}
	if !containsInt64(plan.ChangedLinkedSubIDs, 3972) || !containsInt64(plan.ChangedLinkedSubIDs, 3973) {
		t.Fatalf("3972/3973 are not reported as linked-sub closure rows: %v", plan.ChangedLinkedSubIDs)
	}

	// 全量不变式：被扫中的父任务（ChangedQuestIDs）的每一个 [sub] 子任务都已完成。
	sweptParents := make(map[int64]struct{}, len(plan.ChangedQuestIDs))
	for _, questID := range plan.ChangedQuestIDs {
		sweptParents[questID] = struct{}{}
	}
	dangling := make([]int64, 0)
	for _, definition := range catalog.ordered {
		if definition.MainQuestID <= 0 || normalizeQuestTag(definition.Grade) != "sub" {
			continue
		}
		if _, swept := sweptParents[definition.MainQuestID]; !swept {
			continue
		}
		state, known := plan.Record.States[definition.ID]
		if !known || !isCompletedQuestStatus(state.Status) {
			dangling = append(dangling, definition.ID)
		}
	}
	if len(dangling) != 0 {
		sort.Slice(dangling, func(i, j int) bool { return dangling[i] < dangling[j] })
		t.Fatalf("swept parents left dangling [sub] children: %v", dangling)
	}

	// 闭包行不发奖励、不写结算回执，只写任务行字段。
	for _, questID := range plan.ChangedLinkedSubIDs {
		state := plan.Record.States[questID]
		if !isCompletedQuestStatus(state.Status) || state.ProgressValue != 0 {
			t.Fatalf("linked sub %d state=%+v", questID, state)
		}
		if state.Extra["reward_state"] != finishRewardGranted ||
			state.Extra["auto_completed"] != "true" ||
			state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "90" {
			t.Fatalf("linked sub %d extra=%+v", questID, state.Extra)
		}
		if state.Extra["main_quest_id"] == "" {
			t.Fatalf("linked sub %d has no main_quest_id marker: %+v", questID, state.Extra)
		}
		for _, key := range []string{"settlement_receipt_v1", "settlement_source", "completion_key"} {
			if _, exists := state.Extra[key]; exists {
				t.Fatalf("linked sub %d carries a settlement key %q", questID, key)
			}
		}
	}
	for _, field := range plan.ChangedFields {
		if field != dnfrepo.QuestFieldStates && field != dnfrepo.QuestFieldProgress {
			t.Fatalf("coupon sweep wrote a non-quest field: %q", field)
		}
	}

	// 幂等：同一张记录再规划一次必须没有任何改动。
	replay, err := catalog.PlanCouponMainQuestClear(plan.Record, eligibility, cutoff, CouponClearScopeMainStorySweep, clearedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.ChangedFields) != 0 || len(replay.ChangedLinkedSubIDs) != 0 {
		t.Fatalf("coupon sweep is not idempotent: %+v", replay)
	}

	// 扫完之后可接列表里不能再有 3971（已完成）或 3972/3973（已随父完成）。
	list := catalog.QuestList(eligibility, plan.Record)
	for _, unwanted := range []int32{3971, 3972, 3973} {
		if containsQuestID(list.IDs, unwanted) {
			t.Fatalf("quest %d is still offered after the sweep: ids=%v", unwanted, list.IDs)
		}
	}
	assertClientListSatisfiesAcceptPreflight(t, catalog, eligibility, plan.Record)
}

// TestRealScriptPVFCouponSweepRepairsTheLiveDanglingFarewellMomentState replays
// the live char3 record: 3971 was completed by an older build while 3972/3973
// were left behind. One more sweep must close them, so 运营不需要人工改库。
func TestRealScriptPVFCouponSweepRepairsTheLiveDanglingFarewellMomentState(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	cutoff := CouponClearCutoff(eligibility.Level)
	clearedAt := time.Date(2026, 9, 25, 5, 30, 0, 0, time.UTC)
	// 现场形态 = "整段主线都已经完成（旧构建扫过）+ 3972/3973 还挂着"。
	seeded, err := catalog.PlanCouponMainQuestClear(
		dnfrepo.QuestRecord{CharacterID: "3"},
		eligibility,
		cutoff,
		CouponClearScopeMainStorySweep,
		clearedAt.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	live := seeded.Record
	live.States[3970] = dnfrepo.QuestState{Status: "completed"}
	delete(live.States, 3972)
	delete(live.States, 3973)
	plan, err := catalog.PlanCouponMainQuestClear(live, eligibility, cutoff, CouponClearScopeMainStorySweep, clearedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ChangedQuestIDs) != 0 {
		t.Fatalf("already-completed main quests were rewritten: %v", plan.ChangedQuestIDs)
	}
	if want := []int64{3972, 3973}; !reflect.DeepEqual(plan.ChangedLinkedSubIDs, want) {
		t.Fatalf("dangling [sub] repair=%v want=%v", plan.ChangedLinkedSubIDs, want)
	}
	for _, questID := range []int64{3972, 3973} {
		if state := plan.Record.States[questID]; !isCompletedQuestStatus(state.Status) {
			t.Fatalf("dangling [sub] %d state=%+v", questID, state)
		}
	}
	if plan.Record.States[3971].Status != "completed" {
		t.Fatalf("repair disturbed the parent row: %+v", plan.Record.States[3971])
	}
	list := catalog.QuestList(eligibility, plan.Record)
	for _, unwanted := range []int32{3971, 3972, 3973} {
		if containsQuestID(list.IDs, unwanted) {
			t.Fatalf("quest %d is still offered after the repair: ids=%v", unwanted, list.IDs)
		}
	}
	listBefore := catalog.QuestList(eligibility, live)
	t.Logf("acceptable list before repair=%d after repair=%d (preflight-excluded %d -> %d)",
		len(listBefore.IDs), len(list.IDs), listBefore.PreflightExcluded, list.PreflightExcluded)
	if listBefore.PreflightExcluded != 2 {
		t.Fatalf("the dangling live shape must exclude exactly the two [sub] rows: %d", listBefore.PreflightExcluded)
	}
	if list.PreflightExcluded != 0 {
		t.Fatalf("after the repair nothing may be excluded by the preflight: %d", list.PreflightExcluded)
	}
}

// TestRealScriptPVFCouponCutoffIsTheCopyCeilingAndCoversTheManualSweep pins 决策 2
// 的真档事实：券的上限取券说明里的 90（含），并且它是"任务完成"按钮 89 口径的
// 超集。真档 Script.pvf 里 698 条 `[epic]` 的 `[level]` 上限全是 99，
// 下限没有 90 这一档（见日志），所以 89→90 在这份 PVF 上**不改变被扫集合**；
// 它改的是"口径与文案一致"，真正修掉死节点的是 `[sub]` 闭包。
func TestRealScriptPVFCouponCutoffIsTheCopyCeilingAndCoversTheManualSweep(t *testing.T) {
	catalog, _ := loadRealScriptPVFCatalogForClientList(t)
	eligibility := farewellMomentEligibility()
	couponCutoff := CouponClearCutoff(eligibility.Level)
	manualCutoff := 89 // dnfbridge.currentAutoCompleteMainQuestMaxLevel
	if couponCutoff != 90 || CouponMainQuestClearMaxLevel != 90 {
		t.Fatalf("coupon cutoff=%d ceiling=%d, want 90", couponCutoff, CouponMainQuestClearMaxLevel)
	}
	if manualCutoff != 89 {
		t.Fatalf("manual cutoff=%d want 89", manualCutoff)
	}
	clearedAt := time.Date(2026, 9, 25, 5, 45, 0, 0, time.UTC)
	sweepSet := func(cutoff int) map[int64]struct{} {
		plan, err := catalog.PlanCouponMainQuestClear(dnfrepo.QuestRecord{CharacterID: "3"}, eligibility, cutoff, CouponClearScopeMainStorySweep, clearedAt)
		if err != nil {
			t.Fatalf("cutoff=%d error=%v", cutoff, err)
		}
		set := make(map[int64]struct{}, len(plan.ChangedQuestIDs))
		for _, questID := range plan.ChangedQuestIDs {
			set[questID] = struct{}{}
		}
		return set
	}
	manual := sweepSet(manualCutoff)
	coupon := sweepSet(couponCutoff)
	for questID := range manual {
		if _, ok := coupon[questID]; !ok {
			t.Fatalf("quest %d is inside the manual sweep but outside the coupon sweep", questID)
		}
	}
	levelFloor := make(map[int]int)
	for _, definition := range catalog.ordered {
		if normalizeQuestTag(definition.Grade) == "epic" && definition.ExposedByNPC != 0 && !definition.IsEvent {
			levelFloor[definition.LevelMin]++
		}
	}
	t.Logf("coupon(90) sweep=%d manual(89) sweep=%d 89-only=%d 90-only=%d level_floor_histogram_near_cutoff=%v",
		len(coupon), len(manual), len(coupon)-len(manual), len(coupon)-len(manual),
		map[int]int{88: levelFloor[88], 89: levelFloor[89], 90: levelFloor[90], 91: levelFloor[91]})
	if levelFloor[90] != 0 {
		t.Fatalf("this PVF now has %d epics at [level] floor 90; re-derive the ceiling split", levelFloor[90])
	}
}

func containsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
