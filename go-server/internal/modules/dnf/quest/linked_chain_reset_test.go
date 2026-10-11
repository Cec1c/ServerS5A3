package quest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6 · J12-b 主线链路（quest clear 家族）重置的单元测试。
//
// 上游蓝本：UP `quest/linked_chain_reset.go:24-139`（本仓移植件见
// linked_chain_reset.go）。计划书 §4.6 测试清单第 2 项（链外任务字节不变）在这里，
// 第 4 项（默认档行为）在 gmtool/api_quest_mutations_test.go。
//
// 判据全部走**真解析**的假 PVF 目录（不是手工拼 Definition），因为"哪些任务是
// 被管理的链接子任务"完全由 PVF 段决定。

// linkedChainTestCatalog 造一份自足的假 PVF 任务目录（角色：level 90 / job 11 / grow 0）：
//
//	100 [epic] 1..99 [quest clear] [int data] 101 102 103 104 107   ← 正常家族
//	101 [sub] [main quest] 100        被管理的链接子任务
//	102 [sub] [main quest] 100        被管理的链接子任务
//	103 [sub] [main quest] 100 + [depend give item]  → **不算**被管理（不由父任务代管）
//	104 [normal] [main quest] 100                    → **不算**（不是无奖励 [sub]）
//	105 [epic] 独立任务（链外）
//	106 [epic] [main quest] 999       父任务不在目录里
//	107 [sub] [main quest] 100 + [reward type] `[item]` → **不算**（有奖励）
//	140 [epic] [quest clear] [int data] 141 142       ← 链数据不完整（142 不在目录里）
//	141 [sub] [main quest] 140        被管理的链接子任务
//	150 [epic] [quest clear] [int data] 151 152       ← 有成员激活不回来
//	151 [sub] 1..99 [main quest] 150  可激活
//	152 [sub] 95..99 [main quest] 150 被管理但 90 级角色接不了
//	160 [epic] 95..99 [quest clear] [int data] 161    ← 父任务不接受 90 级角色
//	161 [sub] 1..99 [main quest] 160
//	200 [epic] [quest clear] [int data] 201           ← 另一条家族（链外干扰项）
//	201 [sub] [main quest] 200
//	300 [epic]（不是 quest clear）                    ← 父任务类型不对
//	301 [sub] [main quest] 300
func linkedChainTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "100 `parent.qst`\n101 `child_a.qst`\n102 `child_b.qst`\n103 `child_give.qst`\n" +
			"104 `child_free.qst`\n105 `standalone.qst`\n106 `orphan.qst`\n107 `rewarded.qst`\n" +
			"140 `mixed_parent.qst`\n141 `mixed_child.qst`\n" +
			"150 `narrow_parent.qst`\n151 `wide_child.qst`\n152 `high_child.qst`\n" +
			"160 `high_parent.qst`\n161 `low_child.qst`\n" +
			"200 `other_parent.qst`\n201 `other_child.qst`\n300 `nonclear_parent.qst`\n301 `nonclear_child.qst`\n",
		"n_quest/parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n101 102 103 104 107\n"),
		"n_quest/child_a.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n100\n"),
		"n_quest/child_b.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n100\n"),
		"n_quest/child_give.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n100\n[depend give item]\n1\n"),
		"n_quest/child_free.qst": questCatalogTestDefinition("[normal]", 1, 99, "[all]",
			"[main quest]\n100\n"),
		"n_quest/standalone.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
		"n_quest/orphan.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[main quest]\n999\n"),
		"n_quest/rewarded.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n100\n[reward type]\n`[item]`\n[reward int data]\n1 2\n"),
		"n_quest/mixed_parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n141 142\n"),
		"n_quest/mixed_child.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n140\n"),
		"n_quest/narrow_parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n151 152\n"),
		"n_quest/wide_child.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n150\n"),
		"n_quest/high_child.qst": questCatalogTestDefinition("[sub]", 95, 99, "[all]",
			"[main quest]\n150\n"),
		"n_quest/high_parent.qst": questCatalogTestDefinition("[epic]", 95, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n161\n"),
		"n_quest/low_child.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n160\n"),
		"n_quest/other_parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n201\n"),
		"n_quest/other_child.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n200\n"),
		"n_quest/nonclear_parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
		"n_quest/nonclear_child.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n300\n"),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatalf("build fixture quest PVF: %v", err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load fixture quest catalog: %v", err)
	}
	return catalog
}

func linkedChainTestEligibility() CharacterEligibility {
	return CharacterEligibility{Level: 90, Job: 11, GrowType: 0}
}

// linkedChainTestRecord 是任务账夹具（含另一条家族的完成行、链外的独立任务与
// 孤儿任务、以及 Progress 里的同名旧行），用来证明"只动本家族"。
func linkedChainTestRecord() dnfrepo.QuestRecord {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return dnfrepo.QuestRecord{
		CharacterID: "1",
		UpdatedAt:   at,
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed", ProgressValue: 0, UpdatedAt: at},
			101: {Status: "completed", ProgressValue: 0, UpdatedAt: at, Extra: map[string]string{"reward_state": "granted"}},
			102: {Status: "active", ProgressValue: 3, UpdatedAt: at},
			103: {Status: "completed", UpdatedAt: at},
			104: {Status: "completed", UpdatedAt: at},
			105: {Status: "active", ProgressValue: 1, UpdatedAt: at, Extra: map[string]string{"kept": "yes"}},
			106: {Status: "active", ProgressValue: 1, UpdatedAt: at},
			107: {Status: "completed", UpdatedAt: at},
			200: {Status: "completed", UpdatedAt: at},
			201: {Status: "completed", UpdatedAt: at},
		},
		Progress: map[int64]dnfrepo.QuestState{
			100: {Status: "completed", UpdatedAt: at},
			101: {Status: "active", ProgressValue: 1, UpdatedAt: at},
			201: {Status: "completed", UpdatedAt: at},
		},
	}
}

// linkedChainTestRecordWithQuests 在夹具记录上补几条 completed 行（用于"行存在、
// 但家族因为资格/数据问题不能整体重建"这一类用例）。
func linkedChainTestRecordWithQuests(questIDs ...int64) dnfrepo.QuestRecord {
	record := linkedChainTestRecord()
	for _, questID := range questIDs {
		record.States[questID] = dnfrepo.QuestState{Status: "completed", UpdatedAt: record.UpdatedAt}
	}
	return record
}

// ---------------------------------------------------------------------------
// 判据同源性：抽取出来的共享判定必须与既有内联表达式逐字等价
// ---------------------------------------------------------------------------

// legacyQuestClearDefinition / legacyLinkedQuestChainChild 是**重构前**内联在
// catalog.go:997-1000 / 1018 与 auto_complete.go:361-364 / 376-378 的那两段表达式
// 的逐字副本。本测试用它们钉住抽取后的共享实现，防止"共享助手被改而调用点没跟上"。
func legacyQuestClearDefinition(definition Definition) bool {
	tag := normalizeQuestTag(definition.Type)
	return tag == "quest clear" || tag == "clear quest"
}

func legacyLinkedQuestChainChild(parent Definition, child Definition) bool {
	return child.MainQuestID == parent.ID && !child.HasDependGiveItem && shouldAutoCompleteNoRewardSubQuest(child)
}

func TestLinkedQuestChainPredicatesMatchLegacyInlineForm(t *testing.T) {
	types := []string{"`[quest clear]`", "`[clear quest]`", "`[Clear Quest]`", "`[hunt monster]`", "", "`[quest clear] `"}
	grades := []string{"`[sub]`", "`[epic]`", "`[normal]`", ""}
	combinations := 0
	for _, parentType := range types {
		parent := Definition{ID: 100, Type: parentType}
		if questClearDefinition(parent) != legacyQuestClearDefinition(parent) {
			t.Fatalf("questClearDefinition(%q) 与既有内联式分叉", parentType)
		}
		for _, childGrade := range grades {
			for _, mainQuest := range []int64{0, 100, 101} {
				for _, dependGive := range []bool{false, true} {
					for _, reward := range []string{"", "`[item]`", "`[grow type]`"} {
						child := Definition{
							ID:                200 + int64(len(childGrade)),
							Grade:             childGrade,
							Type:              "`[meet npc]`",
							MainQuestID:       mainQuest,
							HasDependGiveItem: dependGive,
							RewardType:        reward,
						}
						got := linkedQuestChainChild(parent, child)
						want := legacyLinkedQuestChainChild(parent, child)
						if got != want {
							t.Fatalf("linkedQuestChainChild 与既有内联式分叉: parent=%q child=%+v got=%v want=%v",
								parentType, child, got, want)
						}
						combinations++
					}
				}
			}
		}
	}
	if combinations < 200 {
		t.Fatalf("等价矩阵覆盖不足: %d 组", combinations)
	}
}

// TestManagedLinkedQuestChainIDsMatchesAcceptActivationFamily 证明"链路重置会重建的
// 子任务"与"父任务被接取时会自动激活的子任务"是同一批（同一份判据），
// 差别只允许来自资格过滤（definitionAcceptable）。
func TestManagedLinkedQuestChainIDsMatchesAcceptActivationFamily(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	parent := mustFindDefinition(t, catalog, 100)
	if got := catalog.managedLinkedQuestChainIDs(parent); !reflect.DeepEqual(got, []int64{100, 101, 102}) {
		t.Fatalf("managedLinkedQuestChainIDs(100) = %v, want [100 101 102]", got)
	}
	// 父任务被接取时（空任务账）自动激活的子任务必须与家族成员完全一致。
	accept, err := catalog.PlanAccept(linkedChainTestEligibility(), dnfrepo.QuestRecord{}, 100)
	if err != nil {
		t.Fatalf("PlanAccept(100): %v", err)
	}
	linked := make([]int64, 0, len(accept.LinkedSubQuests))
	for _, sub := range accept.LinkedSubQuests {
		linked = append(linked, sub.QuestID)
	}
	if !reflect.DeepEqual(linked, []int64{101, 102}) {
		t.Fatalf("PlanAccept(100) 自动激活的子任务 = %v, want [101 102]", linked)
	}
	// 资格过滤的那一半：152 是"被管理"的（PVF 链接完整），但 90 级角色接不了它。
	narrow := mustFindDefinition(t, catalog, 150)
	if got := catalog.managedLinkedQuestChainIDs(narrow); !reflect.DeepEqual(got, []int64{150, 151, 152}) {
		t.Fatalf("managedLinkedQuestChainIDs(150) = %v, want [150 151 152]", got)
	}
	accept, err = catalog.PlanAccept(linkedChainTestEligibility(), dnfrepo.QuestRecord{}, 150)
	if err != nil {
		t.Fatalf("PlanAccept(150): %v", err)
	}
	if len(accept.LinkedSubQuests) != 1 || accept.LinkedSubQuests[0].QuestID != 151 {
		t.Fatalf("PlanAccept(150) 自动激活 = %+v, want [151]（152 的等级窗口 95..99）", accept.LinkedSubQuests)
	}
	if got := catalog.linkedQuestChainUnresolvedChildIDs(narrow); len(got) != 0 {
		t.Fatalf("150 的 [int data] 没有无法解析的成员: %v", got)
	}
	mixed := mustFindDefinition(t, catalog, 140)
	if got := catalog.linkedQuestChainUnresolvedChildIDs(mixed); !reflect.DeepEqual(got, []int64{142}) {
		t.Fatalf("linkedQuestChainUnresolvedChildIDs(140) = %v, want [142]", got)
	}
}

// ---------------------------------------------------------------------------
// 成功路径：删整族 + 按 PVF 重新激活一代
// ---------------------------------------------------------------------------

func TestPlanLinkedQuestChainResetRemovesWholeFamilyAndReactivatesOne(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	record := linkedChainTestRecord()
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

	plan, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), record, 101, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ParentQuestID != 100 {
		t.Fatalf("parent = %d, want 100", plan.ParentQuestID)
	}
	if !reflect.DeepEqual(plan.RemovedQuestIDs, []int64{100, 101, 102}) {
		t.Fatalf("removed = %v, want [100 101 102]", plan.RemovedQuestIDs)
	}
	if !reflect.DeepEqual(plan.ReactivatedQuestIDs, []int64{100, 101, 102}) {
		t.Fatalf("reactivated = %v, want [100 101 102]", plan.ReactivatedQuestIDs)
	}
	if !reflect.DeepEqual(plan.ChangedFields, []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress}) {
		t.Fatalf("changed fields = %v", plan.ChangedFields)
	}
	if !plan.Record.UpdatedAt.Equal(resetAt) {
		t.Fatalf("updated at = %v", plan.Record.UpdatedAt)
	}

	// 重新激活的一代必须与 PlanAccept（真实接取路径的同一权威）逐字段一致。
	expected := linkedChainTestRecord()
	for _, questID := range []int64{100, 101, 102} {
		delete(expected.States, questID)
		delete(expected.Progress, questID)
	}
	accept, err := catalog.PlanAccept(linkedChainTestEligibility(), expected, 100)
	if err != nil {
		t.Fatalf("PlanAccept(100): %v", err)
	}
	parentState := plan.Record.States[100]
	if parentState.Status != "active" || parentState.ProgressValue != int64(accept.InitTrigger) ||
		!parentState.UpdatedAt.Equal(resetAt) {
		t.Fatalf("父任务重新激活的状态 = %+v, want active trigger=%d", parentState, accept.InitTrigger)
	}
	if parentState.Extra["pvf_path"] != "n_quest/parent.qst" || parentState.Extra["quest_type"] != mustFindDefinition(t, catalog, 100).Type {
		t.Fatalf("父任务 Extra = %+v", parentState.Extra)
	}
	if _, marked := parentState.Extra["main_quest_id"]; marked {
		t.Fatalf("父任务不得带 main_quest_id: %+v", parentState.Extra)
	}
	if len(accept.LinkedSubQuests) != 2 {
		t.Fatalf("PlanAccept 自动激活的子任务 = %+v", accept.LinkedSubQuests)
	}
	for index, questID := range []int64{101, 102} {
		linked := accept.LinkedSubQuests[index]
		if linked.QuestID != questID {
			t.Fatalf("PlanAccept 子任务顺序 = %+v", accept.LinkedSubQuests)
		}
		state := plan.Record.States[questID]
		if state.Status != "active" || state.ProgressValue != int64(linked.InitTrigger) {
			t.Fatalf("子任务 %d 状态 = %+v, want active trigger=%d", questID, state, linked.InitTrigger)
		}
		if state.Extra["main_quest_id"] != "100" || state.Extra["auto_activated_by_main_quest"] != "true" {
			t.Fatalf("子任务 %d 缺少父任务标记: %+v", questID, state.Extra)
		}
		definition := mustFindDefinition(t, catalog, questID)
		if state.Extra["pvf_path"] != definition.Path || state.Extra["quest_type"] != definition.Type {
			t.Fatalf("子任务 %d 的 PVF 标记 = %+v, want path=%q type=%q", questID, state.Extra, definition.Path, definition.Type)
		}
		// 旧世代的行必须真的没了（Progress 里的同名行也要清）。
		if _, exists := plan.Record.Progress[questID]; exists {
			t.Fatalf("子任务 %d 的 Progress 行没有被清掉", questID)
		}
	}
	// 父任务在 Progress 里的旧行同样必须被清掉。
	if _, exists := plan.Record.Progress[100]; exists {
		t.Fatal("父任务的 Progress 行没有被清掉")
	}
}

func TestPlanLinkedQuestChainResetPreservesRowsOutsideTheChain(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	record := linkedChainTestRecord()
	before := questRecordJSON(t, record)

	plan, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), record, 102, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// 输入记录不被修改。
	if after := questRecordJSON(t, record); after != before {
		t.Fatalf("plan 不得修改输入记录:\nbefore=%s\nafter =%s", before, after)
	}
	// 链外任务行逐条不变（含 [depend give item] 子任务、普通子任务、有奖励子任务、
	// 独立任务、父任务不在目录里的孤儿行、以及另一条家族的完成行）。
	outside := []int64{103, 104, 105, 106, 107, 200, 201}
	for _, questID := range outside {
		if !reflect.DeepEqual(plan.Record.States[questID], record.States[questID]) {
			t.Fatalf("链外任务 %d 的 States 行被改动:\nbefore=%+v\nafter =%+v",
				questID, record.States[questID], plan.Record.States[questID])
		}
		if !reflect.DeepEqual(plan.Record.Progress[questID], record.Progress[questID]) {
			t.Fatalf("链外任务 %d 的 Progress 行被改动:\nbefore=%+v\nafter =%+v",
				questID, record.Progress[questID], plan.Record.Progress[questID])
		}
	}
	// 行数守恒：删 3 行、重建 3 行，链外一条不多一条不少。
	if len(plan.Record.States) != len(record.States) {
		t.Fatalf("States 行数 = %d, want %d", len(plan.Record.States), len(record.States))
	}
	// Progress：家族里只有 100/101 有旧行，且不会被重新写入。
	if len(plan.Record.Progress) != len(record.Progress)-2 {
		t.Fatalf("Progress 行数 = %d, want %d", len(plan.Record.Progress), len(record.Progress)-2)
	}
	for _, questID := range []int64{100, 101, 102} {
		if state, exists := plan.Record.States[questID]; !exists || state.Status != "active" {
			t.Fatalf("家族成员 %d 没有被重建成 active: %+v", questID, state)
		}
	}
}

// ---------------------------------------------------------------------------
// 拒绝路径：每一条都必须**具名**，且 plan 不产生任何写入
// ---------------------------------------------------------------------------

func TestPlanLinkedQuestChainResetRejectsWithNamedReasons(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	base := linkedChainTestRecord()
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
	before := questRecordJSON(t, base)

	cases := []struct {
		name    string
		questID int64
		record  dnfrepo.QuestRecord
		want    error
		parent  int64
		details []int64
	}{
		{name: "不是链接子任务（独立任务）", questID: 105, record: base, want: ErrQuestChainNotLinked},
		{name: "父任务本身不是链接子任务", questID: 100, record: base, want: ErrQuestChainNotLinked},
		{name: "父任务不在目录里", questID: 106, record: base, want: ErrQuestChainParentMissing, parent: 999},
		{name: "父任务不是 quest clear", questID: 301, record: base, want: ErrQuestChainParentNotQuestClear, parent: 300},
		{name: "子任务带 depend give item", questID: 103, record: base, want: ErrQuestChainRequestedNotManaged, parent: 100},
		{name: "子任务不是无奖励 sub", questID: 104, record: base, want: ErrQuestChainRequestedNotManaged, parent: 100},
		{name: "子任务有奖励", questID: 107, record: base, want: ErrQuestChainRequestedNotManaged, parent: 100},
		{name: "请求的任务行不存在", questID: 101, record: dnfrepo.QuestRecord{CharacterID: "1"}, want: ErrQuestChainRowMissing, parent: 100},
		{name: "链数据不完整（[int data] 指向目录外任务）", questID: 141, record: linkedChainTestRecordWithQuests(141), want: ErrQuestChainChildUnresolved, parent: 140, details: []int64{142}},
		{name: "家族成员激活不回来", questID: 151, record: linkedChainTestRecordWithQuests(151), want: ErrQuestChainIncompleteReactivation, parent: 150, details: []int64{152}},
	}
	for _, testCase := range cases {
		plan, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), testCase.record, testCase.questID, resetAt)
		if err == nil {
			t.Fatalf("%s: 期望被拒绝，实际成功 plan=%+v", testCase.name, plan)
		}
		if !errors.Is(err, testCase.want) {
			t.Fatalf("%s: err = %v, want %v", testCase.name, err, testCase.want)
		}
		var rejection *LinkedQuestChainResetRejection
		if !errors.As(err, &rejection) {
			t.Fatalf("%s: 拒绝必须是具名的 *LinkedQuestChainResetRejection，实际 %T", testCase.name, err)
		}
		if rejection.QuestID != testCase.questID {
			t.Fatalf("%s: 拒绝没有点名请求的 quest id: %+v", testCase.name, rejection)
		}
		if testCase.parent != 0 && rejection.ParentQuestID != testCase.parent {
			t.Fatalf("%s: 拒绝点名的父任务 = %d, want %d", testCase.name, rejection.ParentQuestID, testCase.parent)
		}
		if len(testCase.details) != 0 && !reflect.DeepEqual(rejection.Details, testCase.details) {
			t.Fatalf("%s: 拒绝点名的细节 = %v, want %v", testCase.name, rejection.Details, testCase.details)
		}
		// 拒绝时 plan 必须是零值（调用方据此保证零写入）。
		if plan.Record.States != nil || plan.Record.Progress != nil || len(plan.RemovedQuestIDs) != 0 ||
			len(plan.ReactivatedQuestIDs) != 0 || len(plan.ChangedFields) != 0 {
			t.Fatalf("%s: 拒绝路径返回了非空 plan: %+v", testCase.name, plan)
		}
	}
	if after := questRecordJSON(t, base); after != before {
		t.Fatalf("plan 必须零写入，夹具记录却变了:\nbefore=%s\nafter =%s", before, after)
	}
}

func TestPlanLinkedQuestChainResetRejectsUnknownQuestNilCatalogAndUnacceptableParent(t *testing.T) {
	catalog := linkedChainTestCatalog(t)

	// 不在 PVF 的任务：沿用上游的 ErrQuestDefinitionMissing（面板文案点名 Script.pvf）。
	if _, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), linkedChainTestRecord(), 999, time.Now()); !errors.Is(err, ErrQuestDefinitionMissing) {
		t.Fatalf("不在 PVF 的任务 err = %v, want ErrQuestDefinitionMissing", err)
	}
	var empty *Catalog
	if _, err := empty.PlanLinkedQuestChainReset(linkedChainTestEligibility(), dnfrepo.QuestRecord{}, 101, time.Now()); !errors.Is(err, ErrCatalogEmpty) {
		t.Fatalf("nil catalog err = %v, want ErrCatalogEmpty", err)
	}
	// 父任务（160 的等级窗口 95..99）不接受当前角色 —— 必须冒泡 PlanAccept 的错误，
	// 绝不"删掉家族再看情况"。
	if _, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), linkedChainTestRecordWithQuests(161), 161, time.Now()); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("父任务不接受角色时 err = %v, want ErrQuestNotAcceptable", err)
	}
	// 父任务已完成时 PlanAccept 不接受它（本仓 PlanAccept 只规划"新接取"）——
	// 这正是 reset 必须**先删家族、再规划**的原因。
	completed := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{
		100: {Status: "completed"},
		101: {Status: "completed"},
	}}
	if _, err := catalog.PlanAccept(linkedChainTestEligibility(), completed, 100); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("已完成父任务的 PlanAccept err = %v, want ErrQuestNotAcceptable", err)
	}
	plan, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), completed, 101, time.Now())
	if err != nil {
		t.Fatalf("reset 必须先删后建，err = %v", err)
	}
	if !reflect.DeepEqual(plan.RemovedQuestIDs, []int64{100, 101}) {
		t.Fatalf("removed = %v, want [100 101]（102 本来就没有行）", plan.RemovedQuestIDs)
	}
	if !reflect.DeepEqual(plan.ReactivatedQuestIDs, []int64{100, 101, 102}) {
		t.Fatalf("reactivated = %v, want [100 101 102]", plan.ReactivatedQuestIDs)
	}
}

// ---------------------------------------------------------------------------
// 真档 Script.pvf
// ---------------------------------------------------------------------------

// TestRealScriptPVFManagedLinkedQuestChainFamilies 用真档钉住家族判定：
// 真档里 3146/3971 这两条已知家族的成员、以及"quest clear 家族只做父任务"
// 这条数据面不变量（本次取证：clear roots=104 / clear children=0）。
func TestRealScriptPVFManagedLinkedQuestChainFamilies(t *testing.T) {
	catalog := p6RealPVFCatalog(t)

	for _, expected := range []struct {
		parent   int64
		children []int64
		path     string
	}{
		{parent: 3146, children: []int64{3157, 3054}, path: "n_quest/quest_renewal/epic_quest/elvengard/elvengard_epic_02.qst"},
		{parent: 3971, children: []int64{3972, 3973}, path: "n_quest/quest_renewal/epic_quest/devildom/timebeast/timebeast_epic_25.qst"},
	} {
		parent := mustFindDefinition(t, catalog, expected.parent)
		if parent.Path != expected.path {
			t.Fatalf("任务 %d 的路径 = %q, want %q", expected.parent, parent.Path, expected.path)
		}
		want := append([]int64{expected.parent}, expected.children...)
		if got := catalog.managedLinkedQuestChainIDs(parent); !reflect.DeepEqual(got, want) {
			t.Fatalf("managedLinkedQuestChainIDs(%d) = %v, want %v", expected.parent, got, want)
		}
	}

	roots, children, families, managedChildren, unresolvedOnlyChildless := 0, 0, 0, 0, 0
	for _, definition := range catalog.ordered {
		if !questClearDefinition(definition) {
			continue
		}
		if definition.MainQuestID > 0 {
			children++
			continue
		}
		roots++
		family := catalog.managedLinkedQuestChainIDs(definition)
		unresolved := catalog.linkedQuestChainUnresolvedChildIDs(definition)
		if len(unresolved) != 0 {
			if len(family) != 1 {
				t.Fatalf("父任务 %d 既有无法解析的子任务 %v、又有被管理子任务 %v", definition.ID, unresolved, family)
			}
			unresolvedOnlyChildless++
			continue
		}
		if len(family) > 1 {
			families++
			managedChildren += len(family) - 1
		}
	}
	t.Logf("real PVF quest-clear roots=%d children=%d families=%d managed children=%d unresolved-only-childless=%d",
		roots, children, families, managedChildren, unresolvedOnlyChildless)
	if children != 0 {
		t.Fatalf("真档里出现了带 [main quest] 的 quest clear 任务 %d 个：家族判定要重新取证", children)
	}
	if families != 32 || managedChildren != 67 {
		t.Fatalf("真档家族计数 = %d 家族 / %d 子任务，want 32 / 67（数据面变了要重新取证）", families, managedChildren)
	}
	if unresolvedOnlyChildless != 3 {
		t.Fatalf("真档里 [int data] 指向目录外任务的父任务 = %d 个，want 3（6511/6514/6538）", unresolvedOnlyChildless)
	}

	// 口径差异的**数据面**取证（只记录、不断言）：上游还有一条更宽的
	// shouldActivateLinkedSubQuest（UP auto_complete.go:544-556：无奖励 [sub] **或**
	// `[sub]` + `[not give exp quest]` + 奖励可解析），本仓没有这条，用的是既有窄口径
	// shouldAutoCompleteNoRewardSubQuest。差集就是上游注释里说的"无经验子任务带普通
	// 道具奖励（分阶段的任务道具产出者）"。
	wideOnly := 0
	wideOnlyDetails := make([]string, 0, 8)
	for _, definition := range catalog.ordered {
		if !questClearDefinition(definition) || definition.MainQuestID > 0 {
			continue
		}
		for _, childID := range definition.IntData {
			if childID <= 0 {
				continue
			}
			child, ok := catalog.Find(childID)
			if !ok || child.MainQuestID != definition.ID || child.HasDependGiveItem {
				continue
			}
			if linkedQuestChainChild(definition, child) {
				continue
			}
			if normalizeQuestTag(child.Grade) == "sub" && child.NoExperience && child.RewardParseError == "" {
				wideOnly++
				if len(wideOnlyDetails) < 8 {
					wideOnlyDetails = append(wideOnlyDetails, fmt.Sprintf("%d->%d(%s)", definition.ID, childID, child.RewardType))
				}
			}
		}
	}
	t.Logf("上游宽口径会代管、本仓窄口径不代管的子任务 = %d 个 %v（对这些子任务请求链路重置会明确拒绝 chain_requested_not_managed，而不是只重置一半）",
		wideOnly, wideOnlyDetails)
}

// TestRealScriptPVFManagedLinkedQuestChainFamiliesDump 把真档 32 条家族按父任务 id
// 打印一次，给交付报告留一份可复核的真档证据（断言规模不变）。
func TestRealScriptPVFManagedLinkedQuestChainFamiliesDump(t *testing.T) {
	catalog := p6RealPVFCatalog(t)
	lines := make([]string, 0, 32)
	for _, definition := range catalog.ordered {
		if !questClearDefinition(definition) || definition.MainQuestID > 0 {
			continue
		}
		family := catalog.managedLinkedQuestChainIDs(definition)
		if len(family) <= 1 {
			continue
		}
		lines = append(lines, int64Text(definition.ID)+" -> "+int64Text(int64(len(family)-1))+" children")
	}
	sort.Strings(lines)
	if len(lines) != 32 {
		t.Fatalf("真档家族数 = %d, want 32", len(lines))
	}
	for _, line := range lines {
		t.Logf("real family %s", line)
	}
}

// TestRealScriptPVFPlanLinkedQuestChainResetReactivatesTheWholeFamily 在真档上跑通
// 成功路径与两条具名拒绝：挑能在真 PVF 上被重新激活的家族（数据驱动搜索，不写死
// 期望值），断言整族被重建、链外行一个字节不变。
func TestRealScriptPVFPlanLinkedQuestChainResetReactivatesTheWholeFamily(t *testing.T) {
	catalog := p6RealPVFCatalog(t)

	type candidate struct {
		parent   int64
		children []int64
		level    int
		job      int
		grow     int
	}
	found := make([]candidate, 0, 3)
	for _, definition := range catalog.ordered {
		if len(found) >= 3 {
			break
		}
		if !questClearDefinition(definition) || definition.MainQuestID > 0 {
			continue
		}
		family := catalog.managedLinkedQuestChainIDs(definition)
		if len(family) <= 1 || len(catalog.linkedQuestChainUnresolvedChildIDs(definition)) != 0 {
			continue
		}
	search:
		for level := 1; level <= 99; level++ {
			for job := 0; job < 16; job++ {
				for _, grow := range []int{0x00, 0x10, 0x11, 0x21} {
					// 搜索用的是 reset **之后**的账（家族已删）：PlanAccept 只规划
					// 新接取，已完成的行会让父任务不可接。
					probe := realFamilyRecord(definition)
					for _, member := range family {
						delete(probe.States, member)
					}
					if _, err := catalog.PlanAccept(CharacterEligibility{Level: level, Job: job, GrowType: grow}, probe, definition.ID); err != nil {
						continue
					}
					found = append(found, candidate{
						parent:   definition.ID,
						children: append([]int64(nil), family[1:]...),
						level:    level,
						job:      job,
						grow:     grow,
					})
					break search
				}
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("真档里找不到任何可被重新激活的 quest clear 家族：链路重置的成功路径未取证")
	}

	for _, testCase := range found {
		parent := mustFindDefinition(t, catalog, testCase.parent)
		record := realFamilyRecord(parent)
		eligibility := CharacterEligibility{Level: testCase.level, Job: testCase.job, GrowType: testCase.grow}
		resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
		family := append([]int64{testCase.parent}, testCase.children...)

		plan, err := catalog.PlanLinkedQuestChainReset(eligibility, record, testCase.children[0], resetAt)
		if err != nil {
			t.Fatalf("真档家族 %v 重置失败: %v", family, err)
		}
		if plan.ParentQuestID != testCase.parent {
			t.Fatalf("父任务 = %d, want %d", plan.ParentQuestID, testCase.parent)
		}
		if !reflect.DeepEqual(plan.ReactivatedQuestIDs, family) {
			t.Fatalf("重新激活 = %v, want %v", plan.ReactivatedQuestIDs, family)
		}
		sortedFamily := append([]int64(nil), family...)
		sort.Slice(sortedFamily, func(i, j int) bool { return sortedFamily[i] < sortedFamily[j] })
		if !reflect.DeepEqual(plan.RemovedQuestIDs, sortedFamily) {
			t.Fatalf("删除 = %v, want %v（升序）", plan.RemovedQuestIDs, sortedFamily)
		}
		if state := plan.Record.States[testCase.parent]; state.Status != "active" {
			t.Fatalf("父任务 %d 没有被重建为 active: %+v", testCase.parent, state)
		}
		for _, childID := range testCase.children {
			state, ok := plan.Record.States[childID]
			if !ok || state.Status != "active" {
				t.Fatalf("子任务 %d 没有被重建为 active: %+v found=%v", childID, state, ok)
			}
			if state.Extra["main_quest_id"] != int64Text(testCase.parent) || state.Extra["auto_activated_by_main_quest"] != "true" {
				t.Fatalf("子任务 %d 缺少父任务标记: %+v", childID, state.Extra)
			}
			if _, exists := plan.Record.Progress[childID]; exists {
				t.Fatalf("子任务 %d 的 Progress 行没有被清掉", childID)
			}
		}
		// 链外任务行一个字节不变（夹具里含父任务前置组的完成行）。
		for questID, state := range record.States {
			if inInt64List(family, questID) {
				continue
			}
			if !reflect.DeepEqual(plan.Record.States[questID], state) {
				t.Fatalf("家族 %v 重置时链外任务 %d 被改动:\nbefore=%+v\nafter =%+v",
					family, questID, state, plan.Record.States[questID])
			}
		}
		t.Logf("real chain reset ok: parent=%d children=%v level=%d job=%d grow=%#x removed=%v",
			testCase.parent, testCase.children, testCase.level, testCase.job, testCase.grow, plan.RemovedQuestIDs)
	}

	// 真档上的具名拒绝：请求父任务本身（quest clear 父任务没有 [main quest]）。
	first := found[0]
	parent := mustFindDefinition(t, catalog, first.parent)
	eligibility := CharacterEligibility{Level: first.level, Job: first.job, GrowType: first.grow}
	if _, err := catalog.PlanLinkedQuestChainReset(eligibility, realFamilyRecord(parent), first.parent, time.Now()); !errors.Is(err, ErrQuestChainNotLinked) {
		t.Fatalf("请求父任务本身 err = %v, want ErrQuestChainNotLinked", err)
	}
	// 真档上的具名拒绝：请求的任务不在任务账里。
	if _, err := catalog.PlanLinkedQuestChainReset(eligibility, dnfrepo.QuestRecord{CharacterID: "1"}, first.children[0], time.Now()); !errors.Is(err, ErrQuestChainRowMissing) {
		t.Fatalf("空任务账 err = %v, want ErrQuestChainRowMissing", err)
	}
}

// realFamilyRecord 造一份"家族已完成 + 父任务前置组已满足"的任务账（前置条件直接来自
// PVF，不写死）。
func realFamilyRecord(parent Definition) dnfrepo.QuestRecord {
	states := map[int64]dnfrepo.QuestState{parent.ID: {Status: "completed"}}
	for _, childID := range parent.IntData {
		if childID > 0 {
			states[childID] = dnfrepo.QuestState{Status: "completed"}
		}
	}
	for _, group := range parent.PreRequiredGroups {
		for _, questID := range group {
			if questID > 0 {
				states[questID] = dnfrepo.QuestState{Status: "completed"}
			}
		}
	}
	return dnfrepo.QuestRecord{CharacterID: "1", States: states}
}

func mustFindDefinition(t *testing.T, catalog *Catalog, questID int64) Definition {
	t.Helper()
	definition, ok := catalog.Find(questID)
	if !ok {
		t.Fatalf("quest %d is not in the catalog", questID)
	}
	return definition
}

func inInt64List(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
