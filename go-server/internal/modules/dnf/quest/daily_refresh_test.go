package quest

import (
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// dailyRefreshBacktick 是 PVF 列表/定义里的字段分隔符，避免测试源码里出现裸反引号。
const dailyRefreshBacktick = "\x60"

func dailyRefreshListEntry(id int, name string) string {
	return strconv.Itoa(id) + " " + dailyRefreshBacktick + name + dailyRefreshBacktick + "\n"
}

// dailyRefreshCatalog 构造一份覆盖全部每日档族与两个非每日对照的小型固定目录。
func dailyRefreshCatalog(t *testing.T) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: dailyRefreshListEntry(101, "daily.qst") +
			dailyRefreshListEntry(102, "daily_random.qst") +
			dailyRefreshListEntry(103, "special_daily.qst") +
			dailyRefreshListEntry(104, "daily_mission.qst") +
			dailyRefreshListEntry(105, "repeat.qst") +
			dailyRefreshListEntry(106, "ordinary.qst") +
			dailyRefreshListEntry(107, "undated_daily.qst") +
			dailyRefreshListEntry(108, "future_daily.qst"),
		"n_quest/daily.qst":         questCatalogTestDefinition("[daily]", 1, 99, "[all]", ""),
		"n_quest/daily_random.qst":  questCatalogTestDefinition("[daily random]", 1, 99, "[all]", ""),
		"n_quest/special_daily.qst": questCatalogTestDefinition("[special daily]", 1, 99, "[all]", ""),
		"n_quest/daily_mission.qst": questCatalogTestDefinition("[daily mission]", 1, 99, "[all]", ""),
		"n_quest/repeat.qst":        questCatalogTestDefinition("[normaly repeat]", 1, 99, "[all]", ""),
		"n_quest/ordinary.qst":      questCatalogTestDefinition("[epic]", 1, 99, "[all]", ""),
		"n_quest/undated_daily.qst": questCatalogTestDefinition("[daily mission]", 1, 99, "[all]", ""),
		"n_quest/future_daily.qst":  questCatalogTestDefinition("[daily]", 1, 99, "[all]", ""),
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

func TestDailyQuestGradeClassification(t *testing.T) {
	// 读侧投影的日键是持久化契约：这里把字面量钉住，回归用例在旧源码上也能编译。
	if QuestDailyCompletionDayKey != "quest_daily_completed_day_v1" {
		t.Fatalf("daily completion day key=%q", QuestDailyCompletionDayKey)
	}
	for _, grade := range []string{"[daily]", "[daily random]", "[special daily]", "[special daily event]", "[daily mission]", "daily", " daily mission "} {
		if !isDailyQuestGrade(grade) {
			t.Fatalf("grade %q must classify as daily", grade)
		}
	}
	// normaly repeat 在参考实现里是“立即重复”，不是每日档；普通 grade 更不能当成每日。
	for _, grade := range []string{"[normaly repeat]", "[epic]", "[mission]", "[sub]", "", "[daily mission log]"} {
		if isDailyQuestGrade(grade) {
			t.Fatalf("grade %q must not classify as daily", grade)
		}
	}
}

func TestProjectDailyQuestRolloverDropsEarlierServiceDayDailyCompletion(t *testing.T) {
	catalog := dailyRefreshCatalog(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	today := strconv.FormatInt(premium.ServiceDayID(now), 10)
	earlier := now.Add(-24 * time.Hour)
	later := now.Add(24 * time.Hour)

	record := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		101: {Status: "completed", Extra: map[string]string{QuestDailyCompletionDayKey: today}},
		104: {Status: "completed", UpdatedAt: earlier},
		105: {Status: "completed", UpdatedAt: earlier},
		106: {Status: "completed", UpdatedAt: earlier},
		107: {Status: "completed"},
		108: {Status: "completed", UpdatedAt: later},
	}}
	projected := catalog.ProjectDailyQuestRollover(record, now)

	if _, kept := projected.States[101]; !kept {
		t.Fatal("today's daily completion must stay visible")
	}
	// 104 没有显式日键，完成时间（昨天）就是当时写下的持久化证据。
	if _, kept := projected.States[104]; kept {
		t.Fatal("earlier service day daily completion must be projected away")
	}
	// 107 没有任何完成日证据，108 的日期在未来（时钟回拨/脏数据）：两者都必须保持
	// 完成，否则已发奖的每日任务会在证据不足或时钟回拨时重新变成可接、重复发奖。
	for _, id := range []int64{107, 108} {
		if _, kept := projected.States[id]; !kept {
			t.Fatalf("daily completion %d without an earlier service day must stay completed", id)
		}
	}
	// 立即重复与普通完成都不是每日档，不参与跨日投影。
	for _, id := range []int64{105, 106} {
		if _, kept := projected.States[id]; !kept {
			t.Fatalf("non-daily completion %d must stay completed", id)
		}
	}
	if _, original := record.States[104]; !original {
		t.Fatal("projection must not mutate the caller's record")
	}
}

func TestCatalogDailyMissionRolloverAndSameDayCompletionGate(t *testing.T) {
	catalog := dailyRefreshCatalog(t)
	character := CharacterEligibility{Level: 90, Job: 2, GrowType: 0}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	stale := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		104: {Status: "completed", UpdatedAt: now.Add(-24 * time.Hour)},
	}}
	if !containsQuestID(catalog.QuestListAt(character, stale, now).IDs, int32(104)) {
		t.Fatal("daily mission completed on an earlier service day must return to the acceptable list")
	}
	if decision, err := catalog.DecideAcceptAt(character, stale, 104, now); err != nil || decision != AcceptNew {
		t.Fatalf("stale daily decision=%v err=%v, want AcceptNew", decision, err)
	}

	// 同一服务日的完成仍然拦下：daily mission 不在 repeatableGrade 里，这条是既有规则
	// （本次修复只负责让更早服务日的完成不再永久拦住任务）。
	claimedToday := dnfrepo.QuestRecord{CharacterID: "6", States: map[int64]dnfrepo.QuestState{
		104: {Status: "completed", UpdatedAt: now,
			Extra: map[string]string{QuestDailyCompletionDayKey: strconv.FormatInt(premium.ServiceDayID(now), 10)}},
	}}
	if containsQuestID(catalog.QuestListAt(character, claimedToday, now).IDs, int32(104)) {
		t.Fatal("daily mission claimed today must not be offered again the same service day")
	}
	if decision, err := catalog.DecideAcceptAt(character, claimedToday, 104, now); decision != AcceptRejected {
		t.Fatalf("same-day daily re-accept decision=%v err=%v, want rejected", decision, err)
	}
}

func TestMarkDailyQuestCompletionStampsOnlyDailyGrades(t *testing.T) {
	catalog := dailyRefreshCatalog(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	want := strconv.FormatInt(premium.ServiceDayID(now), 10)

	dailyDefinition, known := catalog.Find(104)
	if !known {
		t.Fatal("quest 104 missing")
	}
	dailyState := dnfrepo.QuestState{Status: "completed"}
	markDailyQuestCompletion(&dailyState, dailyDefinition, now)
	if got := dailyState.Extra[QuestDailyCompletionDayKey]; got != want {
		t.Fatalf("daily day stamp=%q, want %q", got, want)
	}

	ordinaryDefinition, known := catalog.Find(106)
	if !known {
		t.Fatal("quest 106 missing")
	}
	ordinaryState := dnfrepo.QuestState{Status: "completed"}
	markDailyQuestCompletion(&ordinaryState, ordinaryDefinition, now)
	if _, stamped := ordinaryState.Extra[QuestDailyCompletionDayKey]; stamped {
		t.Fatal("non-daily completion must not receive a daily day stamp")
	}
}
