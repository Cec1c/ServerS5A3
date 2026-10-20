package quest

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// p6RealPVFCatalog 装载真档 Script.pvf 的任务目录（P6 的真档用例共用；需要
// DNFBRIDGE_REAL_PVF_SMOKE 指向 Script.pvf，与 P1/P5 的真档用例同一门控方式）。
func p6RealPVFCatalog(t *testing.T) *Catalog {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the P6 profession/chain resets against the runtime PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real PVF: %v", err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatalf("build real quest index: %v", err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load real quest catalog: %v", err)
	}
	return catalog
}

// P6 · J12-a 职业任务账重置的单元测试。
//
// 与上游 profession_reset_test.go 的两个用例一一对应（按本仓惯例改为**真解析**
// 的目录，而不是手工拼 Catalog 结构体：判据必须走 PVF 的 `[job change quest]`）：
//
//	UP:11 TestPlanBaseProfessionQuestResetClearsClassAndAwakeningOnly
//	UP:67 TestPlanBaseProfessionQuestResetNoopPreservesTimestamp
//
// 另加计划书 §4.6 测试清单第 1 项（plan 零写库 / 纯投影）。

// professionResetTestCatalog 造一份自足的假 PVF 任务目录：
//
//	10 [job change quest] 1   第一职业
//	11 [job change quest] 2   第一次觉醒
//	12 [job change quest] 3   第二次觉醒
//	13 [job change quest] 20  专家职业（故意不动）
//	14 普通任务（无 [job change quest]）
//	99 不在目录里（任务账里存在 -> 不删、不猜）
func professionResetTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList:            "10 `class.qst`\n11 `first.qst`\n12 `second.qst`\n13 `expert.qst`\n14 `ordinary.qst`\n",
		"n_quest/class.qst":    questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[job change quest]\n1\n"),
		"n_quest/first.qst":    questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[job change quest]\n2\n"),
		"n_quest/second.qst":   questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[job change quest]\n3\n"),
		"n_quest/expert.qst":   questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[job change quest]\n20\n"),
		"n_quest/ordinary.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatalf("build fixture quest PVF: %v", err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load fixture quest catalog: %v", err)
	}
	for questID, want := range map[int64]int{10: 1, 11: 2, 12: 3, 13: 20, 14: 0} {
		definition, ok := catalog.Find(questID)
		if !ok || definition.JobChangeQuest != want {
			t.Fatalf("fixture quest %d jobChangeQuest=%d found=%v, want %d", questID, definition.JobChangeQuest, ok, want)
		}
	}
	return catalog
}

func TestPlanBaseProfessionQuestResetClearsClassAndAwakeningOnly(t *testing.T) {
	catalog := professionResetTestCatalog(t)
	resetAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "8",
		UpdatedAt:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		States: map[int64]dnfrepo.QuestState{
			10: {Status: "completed"},
			11: {Status: "active"},
			13: {Status: "active"},
			14: {Status: "completed"},
			99: {Status: "active"},
		},
		Progress: map[int64]dnfrepo.QuestState{
			10: {Status: "active"},
			12: {Status: "completed"},
			13: {Status: "active"},
		},
	}

	plan, err := catalog.PlanBaseProfessionQuestReset(record, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.ResetQuestIDs, []int64{10, 11, 12}) {
		t.Fatalf("reset quest IDs = %v, want [10 11 12]", plan.ResetQuestIDs)
	}
	if !reflect.DeepEqual(plan.ChangedFields, []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress}) {
		t.Fatalf("changed fields = %v", plan.ChangedFields)
	}
	if !plan.Record.UpdatedAt.Equal(resetAt) {
		t.Fatalf("updated at = %v, want %v", plan.Record.UpdatedAt, resetAt)
	}
	if plan.Record.CharacterID != "8" {
		t.Fatalf("character id = %q, want 8", plan.Record.CharacterID)
	}
	for _, questID := range []int64{10, 11, 12} {
		if _, ok := plan.Record.States[questID]; ok {
			t.Fatalf("profession quest %d remains in states", questID)
		}
		if _, ok := plan.Record.Progress[questID]; ok {
			t.Fatalf("profession quest %d remains in progress", questID)
		}
	}
	if _, ok := plan.Record.States[13]; !ok {
		t.Fatal("expert-job state was removed")
	}
	if _, ok := plan.Record.Progress[13]; !ok {
		t.Fatal("expert-job progress row was removed")
	}
	if _, ok := plan.Record.States[14]; !ok {
		t.Fatal("ordinary quest state was removed")
	}
	// 目录里没有定义的任务行**不删**（不猜、不误伤）。
	if _, ok := plan.Record.States[99]; !ok {
		t.Fatal("quest without a PVF definition was removed")
	}
	// 输入记录一个字节都不许动。
	if _, ok := record.States[10]; !ok {
		t.Fatal("input record was mutated")
	}
	if _, ok := record.Progress[12]; !ok {
		t.Fatal("input record progress was mutated")
	}
	if !record.UpdatedAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("input record UpdatedAt was mutated: %v", record.UpdatedAt)
	}
}

func TestPlanBaseProfessionQuestResetNoopPreservesTimestamp(t *testing.T) {
	catalog := professionResetTestCatalog(t)
	updatedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	record := dnfrepo.QuestRecord{
		CharacterID: "8",
		UpdatedAt:   updatedAt,
		States: map[int64]dnfrepo.QuestState{
			13: {Status: "completed"},
			14: {Status: "completed"},
			99: {Status: "active"},
		},
		Progress: map[int64]dnfrepo.QuestState{13: {Status: "completed"}},
	}
	plan, err := catalog.PlanBaseProfessionQuestReset(record, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ResetQuestIDs) != 0 || len(plan.ChangedFields) != 0 {
		t.Fatalf("noop plan = %+v", plan)
	}
	// 上游口径：没有任何职业任务行被删掉时**不动 UpdatedAt**（调用方据此跳过写入）。
	if !plan.Record.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("noop updated at = %v, want %v", plan.Record.UpdatedAt, updatedAt)
	}
	if !reflect.DeepEqual(plan.Record.States, record.States) || !reflect.DeepEqual(plan.Record.Progress, record.Progress) {
		t.Fatalf("noop plan changed the record: %+v", plan.Record)
	}
}

// TestProfessionResetPlanIsPureNoWrite 对应计划书 §4.6 测试清单第 1 项：
// plan 与 apply 分离 —— plan 只返回投影，绝不改动输入记录（输入记录的 map 是引用
// 类型，一旦实现里漏了 CloneQuest 就会在这里被抓到）。
func TestProfessionResetPlanIsPureNoWrite(t *testing.T) {
	catalog := professionResetTestCatalog(t)
	record := dnfrepo.QuestRecord{
		CharacterID: "8",
		UpdatedAt:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		States: map[int64]dnfrepo.QuestState{
			10: {Status: "completed", ProgressValue: 3, Extra: map[string]string{"reward_state": "granted"}},
			13: {Status: "active", Extra: map[string]string{"kept": "yes"}},
		},
		Progress: map[int64]dnfrepo.QuestState{11: {Status: "active"}},
	}
	before := questRecordJSON(t, record)

	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
	plan, err := catalog.PlanBaseProfessionQuestReset(record, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if after := questRecordJSON(t, record); after != before {
		t.Fatalf("plan 必须零写入，输入记录却变了:\nbefore=%s\nafter =%s", before, after)
	}
	// 两次调用必须给出同一份投影（纯函数、与时间参数之外的输入无关）。
	again, err := catalog.PlanBaseProfessionQuestReset(record, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, again) {
		t.Fatalf("plan 不是纯投影:\nfirst =%+v\nsecond=%+v", plan, again)
	}
	// 改 plan.Record 不得影响输入记录（证明 CloneQuest 真的深拷贝了两张表）。
	plan.Record.States[13] = dnfrepo.QuestState{Status: "tampered"}
	if record.States[13].Status != "active" {
		t.Fatalf("plan.Record 与输入记录共享了 map: %+v", record.States)
	}
}

// TestRealScriptPVFBaseProfessionQuestResetKeepsExpertJobs 用真档 Script.pvf 证明
// "只碰 JobChangeQuest 1..3、专家职业（20 那一族）一行不动"这条口径在**真实任务目录**
// 上成立（假 PVF 只能证明代码路径，不能证明数据面）。
func TestRealScriptPVFBaseProfessionQuestResetKeepsExpertJobs(t *testing.T) {
	catalog := p6RealPVFCatalog(t)
	resetAt := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)

	counts := map[int]int{}
	states := make(map[int64]dnfrepo.QuestState)
	progress := make(map[int64]dnfrepo.QuestState)
	wantReset := make(map[int64]struct{})
	expertIDs := make([]int64, 0, 16)
	ordinaryIDs := make([]int64, 0, 4)
	alternate := 0
	for _, definition := range catalog.ordered {
		counts[definition.JobChangeQuest]++
		switch {
		case definition.JobChangeQuest >= 1 && definition.JobChangeQuest <= 3:
			// 一半落在 States、一半落在 Progress，两张表都覆盖。
			if alternate%2 == 0 {
				states[definition.ID] = dnfrepo.QuestState{Status: "completed"}
			} else {
				progress[definition.ID] = dnfrepo.QuestState{Status: "active"}
			}
			wantReset[definition.ID] = struct{}{}
			alternate++
		case definition.JobChangeQuest == 20:
			states[definition.ID] = dnfrepo.QuestState{Status: "completed"}
			expertIDs = append(expertIDs, definition.ID)
		case definition.JobChangeQuest == 0 && len(ordinaryIDs) < 4 && definition.MainQuestID == 0:
			states[definition.ID] = dnfrepo.QuestState{Status: "completed"}
			ordinaryIDs = append(ordinaryIDs, definition.ID)
		}
	}
	if len(wantReset) == 0 || len(expertIDs) == 0 {
		t.Fatalf("真档前提不成立：job-change 1..3=%d expert(20)=%d", len(wantReset), len(expertIDs))
	}
	t.Logf("real catalog job-change counts=%v sampled=%d expert=%v ordinary=%v",
		counts, len(wantReset), expertIDs, ordinaryIDs)

	plan, err := catalog.PlanBaseProfessionQuestReset(dnfrepo.QuestRecord{
		CharacterID: "1",
		States:      states,
		Progress:    progress,
	}, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]struct{}, len(plan.ResetQuestIDs))
	for _, questID := range plan.ResetQuestIDs {
		got[questID] = struct{}{}
	}
	if len(got) != len(wantReset) {
		t.Fatalf("reset ids=%d, want %d", len(got), len(wantReset))
	}
	for questID := range wantReset {
		if _, ok := got[questID]; !ok {
			t.Fatalf("quest %d (job change %d) 没有被清掉", questID, mustJobChangeQuest(t, catalog, questID))
		}
	}
	for _, questID := range expertIDs {
		if _, ok := plan.Record.States[questID]; !ok {
			t.Fatalf("专家职业任务 %d 被误删", questID)
		}
	}
	for _, questID := range ordinaryIDs {
		if _, ok := plan.Record.States[questID]; !ok {
			t.Fatalf("普通任务 %d 被误删", questID)
		}
	}
	if !reflect.DeepEqual(plan.ChangedFields, []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress}) {
		t.Fatalf("changed fields = %v", plan.ChangedFields)
	}
	if !plan.Record.UpdatedAt.Equal(resetAt) {
		t.Fatalf("updated at = %v", plan.Record.UpdatedAt)
	}
}

func mustJobChangeQuest(t *testing.T, catalog *Catalog, questID int64) int {
	t.Helper()
	definition, ok := catalog.Find(questID)
	if !ok {
		t.Fatalf("quest %d is not in the catalog", questID)
	}
	return definition.JobChangeQuest
}

// TestRealScriptPVFJobChangeQuestBucketsMatchTheDumpedCounts 把"第一职业 / 一次觉醒 /
// 二次觉醒"三档在真档里的规模钉住。第一职业包含旧 job 与新版 Elvengard 两组，
// 各 72 条；来源/ID/奖励由 TestRealScriptPVFClassChangeFamiliesMatchSourceDocuments
// 独立核对，确保重置用例覆盖两组而非在空集合上空转。
func TestRealScriptPVFJobChangeQuestBucketsMatchTheDumpedCounts(t *testing.T) {
	catalog := p6RealPVFCatalog(t)
	counts := map[int]int{}
	for _, definition := range catalog.ordered {
		counts[definition.JobChangeQuest]++
	}
	if counts[1] != 144 || counts[2] != 286 || counts[3] != 354 || counts[20] == 0 {
		t.Fatalf("real PVF job-change buckets = %v, want 1:144 2:286 3:354 20:>0", counts)
	}
	t.Logf("real PVF job-change buckets=%v", counts)
}

func questRecordJSON(t *testing.T, record dnfrepo.QuestRecord) string {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal quest record: %v", err)
	}
	return string(raw)
}
