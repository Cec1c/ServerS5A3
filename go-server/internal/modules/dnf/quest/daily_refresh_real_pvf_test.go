package quest

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealPVFStaleDailyMissionCompletionRefreshesNextServiceDay 是 DOC-120/122
// （每日任务 收集/提取安徒恩的细胞未正常刷新、昨日达成条件今日无法确认）的回归。
//
// 固定真档 Script.pvf 里安徒恩每日任务是 grade 「[daily mission]」：
// 10019 n_quest/anton/anton_limit_01.qst「提取安徒恩的神经细胞」、
// 2683 n_quest/anton/daily_anton_upgrade_01.qst「[安徒恩]高级塔尔坦星人的精髓」。
// 修复前：finish 把任务行永久写成 completed，而 repeatableGrade 不含 daily mission，
// 于是 op21 可接列表、接取预检与 op356 完成表都把它当成永久完成 —— 每日任务不再刷新，
// 隔天的完成/确认也随之失效。
//
// 本用例只用修复前就存在的公开 API（QuestList / DecideAccept），所以在旧源码上会失败。
func TestRealPVFStaleDailyMissionCompletionRefreshesNextServiceDay(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
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

	dailyMissions := []int64{10019, 2683}
	for _, id := range dailyMissions {
		definition, found := catalog.Find(id)
		if !found {
			t.Fatalf("quest %d missing from the real PVF catalog", id)
		}
		if grade := normalizeQuestTag(definition.Grade); grade != "daily mission" {
			t.Fatalf("real PVF quest %d grade=%q, want daily mission", id, grade)
		}
	}

	// 现场存档形态：完成行来自修复前的构建（没有显式日键），完成时间落在更早的服务日。
	character := CharacterEligibility{Level: 90, Job: 0, GrowType: 33}
	stale := time.Now().Add(-24 * time.Hour)
	record := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		3777:  {Status: "completed"},
		10019: {Status: "completed", UpdatedAt: stale},
		2683:  {Status: "completed", UpdatedAt: stale},
	}}
	list := catalog.QuestList(character, record)
	for _, id := range dailyMissions {
		if !containsQuestID(list.IDs, int32(id)) {
			t.Fatalf("stale daily mission %d must return to the op21 acceptable list", id)
		}
		if decision, err := catalog.DecideAccept(character, record, id); err != nil || decision != AcceptNew {
			t.Fatalf("stale daily mission %d decision=%v err=%v, want AcceptNew", id, decision, err)
		}
	}

	// 同一服务日的完成仍拦住当天第二次接取：daily mission 不在 repeatableGrade 里，
	// 这条是既有规则；本次修复只让更早服务日的完成不再永久拦住任务。
	today := strconv.FormatInt(premium.ServiceDayID(time.Now()), 10)
	claimed := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		3777:  {Status: "completed"},
		10019: {Status: "completed", UpdatedAt: time.Now(), Extra: map[string]string{"quest_daily_completed_day_v1": today}},
		2683:  {Status: "completed", UpdatedAt: time.Now(), Extra: map[string]string{"quest_daily_completed_day_v1": today}},
	}}
	claimedList := catalog.QuestList(character, claimed)
	for _, id := range dailyMissions {
		if containsQuestID(claimedList.IDs, int32(id)) {
			t.Fatalf("daily mission %d claimed today must not be listed again", id)
		}
		if decision, err := catalog.DecideAccept(character, claimed, id); decision != AcceptRejected {
			t.Fatalf("same-day daily mission %d decision=%v err=%v, want rejected", id, decision, err)
		}
	}

	// 对照：非每日完成（12830 [mission]）不参与跨日投影，仍是完成态且不可接取。
	control := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		12829: {Status: "completed"},
		12830: {Status: "completed", UpdatedAt: stale},
	}}
	if containsQuestID(catalog.QuestList(character, control).IDs, int32(12830)) {
		t.Fatal("non-daily completion must stay completed across service days")
	}
	if decision, err := catalog.DecideAccept(character, control, 12830); decision != AcceptRejected {
		t.Fatalf("non-daily completed quest decision=%v err=%v, want rejected", decision, err)
	}
}
