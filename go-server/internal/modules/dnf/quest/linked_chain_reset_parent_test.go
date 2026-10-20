package quest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6b · 主线链路重置的**父任务入口**（Catalog.PlanLinkedQuestChainResetFromParent）
// 单元测试。
//
// 背景（P6 交付报告 §8-② 自己点名的缺口）：quest clear 家族父任务自己的
// `[main quest]` 是 0，所以 PlanLinkedQuestChainReset 对父任务恒返
// ErrQuestChainNotLinked；操作者在面板上点父任务那一行时只能退化成"删掉父行"，
// 同一家族里已持久化的子任务世代留在库里。
//
// 本文件钉死三件事：
//  1. 父任务入口与子任务入口产出**同一份**重置投影（同一段家族判定 + 同一段重建），
//     既有子任务入口的行为一字节未变（P6 的 linked_chain_reset_test.go 全绿即为证）；
//  2. 没有受管子任务的 quest clear 父任务 / 普通任务 → 具名拒绝 ErrQuestChainNoManagedChildren
//     / ErrQuestChainNotLinked，零写入 —— 绝不编造家族（面板侧据此走普通单条路径）；
//  3. 家族数据不完整（子任务指向目录外 / 重建不回来 / 行不存在）→ 具名拒绝，零写入。
//
// 夹具直接复用 linked_chain_reset_test.go 的 linkedChainTestCatalog /
// linkedChainTestRecord（同一份假 PVF，不新造第二套数据面），另加一个**最小**目录
// 覆盖"父任务存在但没有任何受管子任务"这一格。

// ---------------------------------------------------------------------------
// 成功路径：父任务入口 = 整族重置，且与子任务入口逐字段同投影
// ---------------------------------------------------------------------------

func TestPlanLinkedQuestChainResetFromParentRebuildsTheWholeFamily(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	record := linkedChainTestRecord()
	before := questRecordJSON(t, record)
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

	plan, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), record, 100, resetAt)
	if err != nil {
		t.Fatalf("父任务入口重置失败: %v", err)
	}
	if plan.ParentQuestID != 100 {
		t.Fatalf("parent = %d, want 100（父任务入口的 parentQuestId 就是请求的任务自身）", plan.ParentQuestID)
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
		t.Fatalf("updated at = %v, want %v", plan.Record.UpdatedAt, resetAt)
	}

	// 重建的一代：父 active（不带 main_quest_id）、子 active（带父任务标记）。
	parentState := plan.Record.States[100]
	if parentState.Status != "active" {
		t.Fatalf("父任务没被重建成 active: %+v", parentState)
	}
	if _, marked := parentState.Extra["main_quest_id"]; marked {
		t.Fatalf("父任务不得带 main_quest_id: %+v", parentState.Extra)
	}
	for _, questID := range []int64{101, 102} {
		state, exists := plan.Record.States[questID]
		if !exists || state.Status != "active" {
			t.Fatalf("子任务 %d 没被重建成 active: %+v exists=%v", questID, state, exists)
		}
		if state.Extra["main_quest_id"] != "100" || state.Extra["auto_activated_by_main_quest"] != "true" {
			t.Fatalf("子任务 %d 缺少父任务标记: %+v", questID, state.Extra)
		}
	}
	// 旧世代在两张表里的行都必须消失。
	for _, questID := range []int64{100, 101, 102} {
		if _, exists := plan.Record.Progress[questID]; exists {
			t.Fatalf("家族成员 %d 的 Progress 旧行没有被清掉: %+v", questID, plan.Record.Progress)
		}
	}
	// 链外任务行逐条不变（[depend give item] 子任务、普通子任务、有奖励子任务、独立任务、
	// 父任务不在目录里的孤儿行、另一条家族的完成行）。
	for _, questID := range []int64{103, 104, 105, 106, 107, 200, 201} {
		if !reflect.DeepEqual(plan.Record.States[questID], record.States[questID]) {
			t.Fatalf("链外任务 %d 的 States 行被改动:\nbefore=%+v\nafter =%+v",
				questID, record.States[questID], plan.Record.States[questID])
		}
		if !reflect.DeepEqual(plan.Record.Progress[questID], record.Progress[questID]) {
			t.Fatalf("链外任务 %d 的 Progress 行被改动:\nbefore=%+v\nafter =%+v",
				questID, record.Progress[questID], plan.Record.Progress[questID])
		}
	}
	// plan 是纯投影：输入记录一字节不动。
	if after := questRecordJSON(t, record); after != before {
		t.Fatalf("plan 不得修改输入记录:\nbefore=%s\nafter =%s", before, after)
	}

	// **同源性**：从父任务发起与从它的子任务发起必须是**同一份**投影（同一段家族判定 +
	// 同一段删除/重建），否则两个入口迟早分叉。
	fromChild, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), record, 101, resetAt)
	if err != nil {
		t.Fatalf("子任务入口重置失败: %v", err)
	}
	if !reflect.DeepEqual(plan, fromChild) {
		t.Fatalf("父任务入口与子任务入口的投影分叉:\nparent=%+v\nchild =%+v", plan, fromChild)
	}
}

// TestPlanLinkedQuestChainResetFromParentDelegatesSubQuestsToTheChildEntry 请求的是
// 链接子任务时，父任务入口必须交给**唯一**的子任务入口（行为不许分叉）。
func TestPlanLinkedQuestChainResetFromParentDelegatesSubQuestsToTheChildEntry(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	record := linkedChainTestRecord()
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

	direct, err := catalog.PlanLinkedQuestChainReset(linkedChainTestEligibility(), record, 102, resetAt)
	if err != nil {
		t.Fatalf("子任务入口: %v", err)
	}
	delegated, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), record, 102, resetAt)
	if err != nil {
		t.Fatalf("父任务入口（请求子任务 102）: %v", err)
	}
	if !reflect.DeepEqual(direct, delegated) {
		t.Fatalf("请求子任务时两个入口分叉:\ndirect   =%+v\ndelegated=%+v", direct, delegated)
	}
}

// ---------------------------------------------------------------------------
// 面板接线用的判据：只有"quest clear 家族父任务 + 至少一个受管子任务"为真
// ---------------------------------------------------------------------------

func TestManagedLinkedQuestChainFamilyOnlyAcceptsRootsWithManagedChildren(t *testing.T) {
	catalog := linkedChainTestCatalog(t)

	// 真：quest clear 根 + 至少一个受管子任务。
	for _, expected := range []struct {
		questID int64
		family  []int64
	}{
		{questID: 100, family: []int64{100, 101, 102}},
		{questID: 150, family: []int64{150, 151, 152}},
		{questID: 200, family: []int64{200, 201}},
		// 140 家族里 141 是受管子任务、142 指向目录外：判据看的是"有没有受管子任务"，
		// 数据完整性由 plan 自己具名拒绝（ErrQuestChainChildUnresolved）。
		{questID: 140, family: []int64{140, 141}},
	} {
		family, ok := catalog.ManagedLinkedQuestChainFamily(expected.questID)
		if !ok {
			t.Fatalf("任务 %d 必须被判定为「可整族重置的家族根」", expected.questID)
		}
		if !reflect.DeepEqual(family, expected.family) {
			t.Fatalf("任务 %d 的家族 = %v, want %v", expected.questID, family, expected.family)
		}
		if family[0] != expected.questID {
			t.Fatalf("家族成员必须以请求的任务开头: %v", family)
		}
	}

	// 假：链接子任务、普通任务、非 quest clear 的根、父任务不在目录里的孤儿。
	for _, questID := range []int64{101, 105, 106, 300, 301} {
		if family, ok := catalog.ManagedLinkedQuestChainFamily(questID); ok {
			t.Fatalf("任务 %d 不得被判定为家族根，实际 family=%v", questID, family)
		}
	}
	// 不在目录里的任务同样为假（面板会给 definition_missing，不会走到这里）。
	if _, ok := catalog.ManagedLinkedQuestChainFamily(999); ok {
		t.Fatal("不在 PVF 的任务不得被判定为家族根")
	}
	var empty *Catalog
	if _, ok := empty.ManagedLinkedQuestChainFamily(100); ok {
		t.Fatal("nil catalog 必须返回 false（不 panic、不猜）")
	}

	// 判据与家族判定的唯一实现完全一致：遍历整份目录逐条对拍。
	for _, definition := range catalog.ordered {
		family, ok := catalog.ManagedLinkedQuestChainFamily(definition.ID)
		want := questClearDefinition(definition) && definition.MainQuestID == 0 &&
			len(catalog.managedLinkedQuestChainIDs(definition)) > 1
		if ok != want {
			t.Fatalf("任务 %d 的家族根判定 = %v, want %v（判据必须只是 managedLinkedQuestChainIDs 的封装）",
				definition.ID, ok, want)
		}
		if ok && !reflect.DeepEqual(family, catalog.managedLinkedQuestChainIDs(definition)) {
			t.Fatalf("任务 %d 的家族成员与 managedLinkedQuestChainIDs 分叉: %v", definition.ID, family)
		}
	}
}

// ---------------------------------------------------------------------------
// 拒绝路径：每条都具名、带 id、零写入
// ---------------------------------------------------------------------------

func TestPlanLinkedQuestChainResetFromParentRejectsWithNamedReasons(t *testing.T) {
	catalog := linkedChainTestCatalog(t)
	base := linkedChainTestRecord()
	before := questRecordJSON(t, base)
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		questID int64
		record  dnfrepo.QuestRecord
		want    error
		parent  int64
		details []int64
	}{
		{name: "普通任务（不是 quest clear 根）", questID: 105, record: base, want: ErrQuestChainNotLinked},
		{name: "非 quest clear 的根", questID: 300, record: base, want: ErrQuestChainNotLinked},
		{name: "请求子任务但父任务不在目录里（交给子任务入口）", questID: 106, record: base, want: ErrQuestChainParentMissing, parent: 999},
		{name: "父任务行不存在", questID: 100, record: dnfrepo.QuestRecord{CharacterID: "1"}, want: ErrQuestChainRowMissing, parent: 100},
		{name: "链数据不完整（[int data] 指向目录外任务）", questID: 140, record: linkedChainTestRecordWithQuests(140), want: ErrQuestChainChildUnresolved, parent: 140, details: []int64{142}},
		{name: "家族成员激活不回来", questID: 150, record: linkedChainTestRecordWithQuests(150), want: ErrQuestChainIncompleteReactivation, parent: 150, details: []int64{152}},
	}
	for _, testCase := range cases {
		plan, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), testCase.record, testCase.questID, resetAt)
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

	// 不在 PVF 的任务与 nil catalog 沿用既有哨兵（不是链路拒绝）。
	if _, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), base, 999, resetAt); !errors.Is(err, ErrQuestDefinitionMissing) {
		t.Fatalf("不在 PVF 的任务 err = %v, want ErrQuestDefinitionMissing", err)
	}
	var empty *Catalog
	if _, err := empty.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), dnfrepo.QuestRecord{}, 100, resetAt); !errors.Is(err, ErrCatalogEmpty) {
		t.Fatalf("nil catalog err = %v, want ErrCatalogEmpty", err)
	}
	// 父任务不接受当前角色（160 的等级窗口 95..99）：冒泡 PlanAccept 的错误，
	// 绝不"先删家族再看情况"。
	if _, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(),
		linkedChainTestRecordWithQuests(160), 160, resetAt); !errors.Is(err, ErrQuestNotAcceptable) {
		t.Fatalf("父任务不接受角色时 err = %v, want ErrQuestNotAcceptable", err)
	}
}

// linkedChainParentOnlyTestCatalog 是"父任务存在、但没有任何受管子任务"这一格的**最小**
// 目录（真档里 6511/6514/6538 就是这一类：`[int data]` 指向目录外）：
//
//	400 [epic] [quest clear]                 完全没有 [int data]
//	401 [epic] [quest clear] [int data] 4999 成员指向目录外（悬空引用）
//	402 [epic]（不是 quest clear，也没有 [main quest]）
//	410 [epic] [quest clear] [int data] 411 412   411 受管、412 不受管（[normal]）
//	411 [sub] [main quest] 410
//	412 [normal] [main quest] 410            不是无奖励 [sub] → 不受管
func linkedChainParentOnlyTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "400 `childless.qst`\n401 `dangling.qst`\n402 `plain.qst`\n" +
			"410 `partial.qst`\n411 `managed.qst`\n412 `unmanaged.qst`\n",
		"n_quest/childless.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n"),
		"n_quest/dangling.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n4999\n"),
		"n_quest/plain.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
		"n_quest/partial.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[quest clear]`\n[int data]\n411 412\n"),
		"n_quest/managed.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[main quest]\n410\n"),
		"n_quest/unmanaged.qst": questCatalogTestDefinition("[normal]", 1, 99, "[all]",
			"[main quest]\n410\n"),
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

func TestPlanLinkedQuestChainResetFromParentRejectsRootsWithoutManagedChildren(t *testing.T) {
	catalog := linkedChainParentOnlyTestCatalog(t)
	at := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "1",
		UpdatedAt:   at,
		States: map[int64]dnfrepo.QuestState{
			400: {Status: "completed", UpdatedAt: at},
			401: {Status: "completed", UpdatedAt: at},
			402: {Status: "completed", UpdatedAt: at},
			410: {Status: "completed", UpdatedAt: at},
			411: {Status: "completed", UpdatedAt: at},
			412: {Status: "completed", UpdatedAt: at},
		},
	}
	before := questRecordJSON(t, record)

	// 没有任何受管子任务的 quest clear 父任务：具名拒绝，绝不编造家族。
	for _, questID := range []int64{400, 401} {
		if family, ok := catalog.ManagedLinkedQuestChainFamily(questID); ok {
			t.Fatalf("任务 %d 没有受管子任务，判据不得为真: %v", questID, family)
		}
		plan, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), record, questID, at)
		if !errors.Is(err, ErrQuestChainNoManagedChildren) {
			t.Fatalf("任务 %d err = %v, want ErrQuestChainNoManagedChildren", questID, err)
		}
		var rejection *LinkedQuestChainResetRejection
		if !errors.As(err, &rejection) {
			t.Fatalf("任务 %d 的拒绝必须具名，实际 %T", questID, err)
		}
		if rejection.QuestID != questID || rejection.ParentQuestID != questID {
			t.Fatalf("任务 %d 的拒绝必须点名它自己: %+v", questID, rejection)
		}
		if plan.Record.States != nil || len(plan.RemovedQuestIDs) != 0 {
			t.Fatalf("任务 %d 的拒绝路径返回了非空 plan: %+v", questID, plan)
		}
	}
	// 不是 quest clear 的普通任务：沿用既有的 ErrQuestChainNotLinked。
	if _, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), record, 402, at); !errors.Is(err, ErrQuestChainNotLinked) {
		t.Fatalf("普通任务 err = %v, want ErrQuestChainNotLinked", err)
	}
	if after := questRecordJSON(t, record); after != before {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%s\nafter =%s", before, after)
	}

	// 有一个受管子任务（412 不受管）：家族 = [410 411]，412 一行不动。
	family, ok := catalog.ManagedLinkedQuestChainFamily(410)
	if !ok || !reflect.DeepEqual(family, []int64{410, 411}) {
		t.Fatalf("任务 410 的家族 = %v ok=%v, want [410 411]", family, ok)
	}
	plan, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), record, 410, at)
	if err != nil {
		t.Fatalf("410 父任务入口重置失败: %v", err)
	}
	if !reflect.DeepEqual(plan.RemovedQuestIDs, []int64{410, 411}) {
		t.Fatalf("removed = %v, want [410 411]（412 不受管）", plan.RemovedQuestIDs)
	}
	if !reflect.DeepEqual(plan.ReactivatedQuestIDs, []int64{410, 411}) {
		t.Fatalf("reactivated = %v, want [410 411]", plan.ReactivatedQuestIDs)
	}
	if !reflect.DeepEqual(plan.Record.States[412], record.States[412]) {
		t.Fatalf("不受管的同族引用任务 412 被改动: %+v", plan.Record.States[412])
	}
}

// ---------------------------------------------------------------------------
// 真档 Script.pvf
// ---------------------------------------------------------------------------

// TestRealScriptPVFPlanLinkedQuestChainResetFromParentRebuildsTheWholeFamily 在真档上
// 跑通父任务入口：真档家族 3146 -> 3157 / 3054（P6 的真档家族取证）上，从**父任务**
// 发起与从**子任务**发起必须得到同一份投影；并钉住三条真档事实：
//
//   - quest clear 家族根都没有 `[main quest]`（P6 已取证：children=0）；
//   - 32 条家族里有 3 条（6511/6514/6538）的 `[int data]` 指向目录外、且没有任何受管
//     子任务 —— 父任务入口必须具名拒绝 ErrQuestChainNoManagedChildren，而不是编造家族；
//   - 请求的父任务行不存在 → ErrQuestChainRowMissing（而不是上游的静默空操作）。
func TestRealScriptPVFPlanLinkedQuestChainResetFromParentRebuildsTheWholeFamily(t *testing.T) {
	catalog := p6RealPVFCatalog(t)

	// 数据面：家族根判据的普查（与 P6 的 32 家族清单对得上）。
	roots, families, noManaged, unresolvedOnly := 0, 0, 0, 0
	var firstChildless int64
	for _, definition := range catalog.ordered {
		if !questClearDefinition(definition) || definition.MainQuestID > 0 {
			continue
		}
		roots++
		family, ok := catalog.ManagedLinkedQuestChainFamily(definition.ID)
		if ok {
			families++
			if family[0] != definition.ID {
				t.Fatalf("父任务 %d 的家族首元素 = %d", definition.ID, family[0])
			}
			continue
		}
		noManaged++
		if len(catalog.linkedQuestChainUnresolvedChildIDs(definition)) != 0 {
			unresolvedOnly++
			if firstChildless == 0 {
				firstChildless = definition.ID
			}
		}
	}
	t.Logf("real PVF parent-entry census: roots=%d family-roots=%d no-managed-children=%d unresolved-only=%d",
		roots, families, noManaged, unresolvedOnly)
	if roots == 0 {
		t.Fatal("真档里没有 quest clear 根：家族判据要重新取证")
	}
	if families != 32 {
		t.Fatalf("真档可整族重置的家族根 = %d, want 32（与 P6 的家族清单同源）", families)
	}
	if unresolvedOnly != 3 {
		t.Fatalf("真档 [int data] 指向目录外的父任务 = %d, want 3（6511/6514/6538）", unresolvedOnly)
	}

	// 成功路径：数据驱动挑一个能重新激活的家族（不写死等级/职业）。
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
		family, ok := catalog.ManagedLinkedQuestChainFamily(definition.ID)
		if !ok || len(catalog.linkedQuestChainUnresolvedChildIDs(definition)) != 0 {
			continue
		}
	search:
		for level := 1; level <= 99; level++ {
			for job := 0; job < 16; job++ {
				for _, grow := range []int{0x00, 0x10, 0x11, 0x21} {
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
		t.Fatal("真档里找不到任何可被重新激活的 quest clear 家族：父任务入口的成功路径未取证")
	}
	for _, testCase := range found {
		parent := mustFindDefinition(t, catalog, testCase.parent)
		record := realFamilyRecord(parent)
		eligibility := CharacterEligibility{Level: testCase.level, Job: testCase.job, GrowType: testCase.grow}
		resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
		family := append([]int64{testCase.parent}, testCase.children...)

		plan, err := catalog.PlanLinkedQuestChainResetFromParent(eligibility, record, testCase.parent, resetAt)
		if err != nil {
			t.Fatalf("真档家族 %v 从父任务重置失败: %v", family, err)
		}
		if plan.ParentQuestID != testCase.parent {
			t.Fatalf("parentQuestId = %d, want %d", plan.ParentQuestID, testCase.parent)
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
			t.Fatalf("父任务 %d 没有被重建成 active: %+v", testCase.parent, state)
		}
		for _, childID := range testCase.children {
			state, ok := plan.Record.States[childID]
			if !ok || state.Status != "active" {
				t.Fatalf("子任务 %d 没有被重建成 active: %+v found=%v", childID, state, ok)
			}
			if state.Extra["main_quest_id"] != int64Text(testCase.parent) || state.Extra["auto_activated_by_main_quest"] != "true" {
				t.Fatalf("子任务 %d 缺少父任务标记: %+v", childID, state.Extra)
			}
		}
		// 与子任务入口同投影（真档上也必须同源）。
		fromChild, err := catalog.PlanLinkedQuestChainReset(eligibility, record, testCase.children[0], resetAt)
		if err != nil {
			t.Fatalf("真档家族 %v 从子任务重置失败: %v", family, err)
		}
		if !reflect.DeepEqual(plan, fromChild) {
			t.Fatalf("真档上父/子入口的投影分叉:\nparent=%+v\nchild =%+v", plan, fromChild)
		}
		// 链外任务行一个字节不变。
		for questID, state := range record.States {
			if inInt64List(family, questID) {
				continue
			}
			if !reflect.DeepEqual(plan.Record.States[questID], state) {
				t.Fatalf("家族 %v 重置时链外任务 %d 被改动:\nbefore=%+v\nafter =%+v",
					family, questID, state, plan.Record.States[questID])
			}
		}
		t.Logf("real parent-entry chain reset ok: parent=%d children=%v level=%d job=%d grow=%#x removed=%v",
			testCase.parent, testCase.children, testCase.level, testCase.job, testCase.grow, plan.RemovedQuestIDs)
	}

	// 真档具名拒绝：没有任何受管子任务的 quest clear 根（6511 这一类）。
	if firstChildless != 0 {
		childless := mustFindDefinition(t, catalog, firstChildless)
		record := realFamilyRecord(childless)
		if _, err := catalog.PlanLinkedQuestChainResetFromParent(CharacterEligibility{Level: 90, Job: 0, GrowType: 0},
			record, firstChildless, time.Now().UTC()); !errors.Is(err, ErrQuestChainNoManagedChildren) {
			t.Fatalf("真档 childless 父任务 %d err = %v, want ErrQuestChainNoManagedChildren", firstChildless, err)
		}
		t.Logf("real parent-entry childless rejection ok: parent=%d int_data=%v unresolved=%v",
			firstChildless, childless.IntData, catalog.linkedQuestChainUnresolvedChildIDs(childless))
	}
	// 真档具名拒绝：行不存在 / 请求的父任务在目录里不存在。
	first := found[0]
	if _, err := catalog.PlanLinkedQuestChainResetFromParent(
		CharacterEligibility{Level: first.level, Job: first.job, GrowType: first.grow},
		dnfrepo.QuestRecord{CharacterID: "1"}, first.parent, time.Now().UTC()); !errors.Is(err, ErrQuestChainRowMissing) {
		t.Fatalf("空任务账 err = %v, want ErrQuestChainRowMissing", err)
	}
	// 真档具名拒绝：请求的任务在任务目录里根本不存在（id 现场找，不写死 —— 真档里
	// 连 999 都是有定义的）。
	absentID := int64(0)
	for questID := int64(1); questID <= math.MaxUint16; questID++ {
		if _, found := catalog.Find(questID); !found {
			absentID = questID
			break
		}
	}
	if absentID == 0 {
		t.Fatal("真档任务表覆盖了 1..65535：目录外 id 的拒绝路径未取证")
	}
	if _, err := catalog.PlanLinkedQuestChainResetFromParent(linkedChainTestEligibility(), dnfrepo.QuestRecord{},
		absentID, time.Now().UTC()); !errors.Is(err, ErrQuestDefinitionMissing) {
		t.Fatalf("目录外任务 %d err = %v, want ErrQuestDefinitionMissing", absentID, err)
	}
	t.Logf("real parent-entry missing-definition rejection ok: quest=%d", absentID)
}
