package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// dailyRolloverBacktick 是 PVF 定义里的字段分隔符，避免测试源码出现裸反引号。
const dailyRolloverBacktick = "\x60"

func dailyRolloverListEntry(id int, name string) string {
	return strconv.Itoa(id) + " " + dailyRolloverBacktick + name + dailyRolloverBacktick + "\n"
}

func dailyRolloverQuestDefinition(grade string) string {
	return "[grade]\n" + dailyRolloverBacktick + grade + dailyRolloverBacktick +
		"\n[level]\n1 99\n[job]\n" + dailyRolloverBacktick + "[all]" + dailyRolloverBacktick +
		"\n[exposed by npc]\n1\n"
}

// TestCurrentClearQuestListHidesRolledOverDailyCompletion 是 DOC-120
// （每日任务 收集安徒恩的细胞 未正常刷新）的客户端投影回归。
//
// op356 固定完成表和 op21 可接列表必须共用同一套每日判定：过期的每日完成若仍以
// 完成位下发，客户端就会一直把每日任务当成“已完成”，任务再也不会刷新。本用例只用
// 修复前就存在的入口，所以在旧源码上失败。
func TestCurrentClearQuestListHidesRolledOverDailyCompletion(t *testing.T) {
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, bridgePVFSource{
		dnfquest.DefaultList: dailyRolloverListEntry(3101, "daily.qst") +
			dailyRolloverListEntry(3102, "ordinary.qst") +
			dailyRolloverListEntry(3103, "future_daily.qst") +
			dailyRolloverListEntry(3104, "undated_daily.qst"),
		"n_quest/daily.qst":         dailyRolloverQuestDefinition("[daily mission]"),
		"n_quest/ordinary.qst":      dailyRolloverQuestDefinition("[epic]"),
		"n_quest/future_daily.qst":  dailyRolloverQuestDefinition("[daily mission]"),
		"n_quest/undated_daily.qst": dailyRolloverQuestDefinition("[daily]"),
	}, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}

	repositories := dnfrepomemory.NewMemoryGroup()
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		3101: {Status: "completed", UpdatedAt: clock.Add(-24 * time.Hour)},
		3102: {Status: "completed", UpdatedAt: clock.Add(-24 * time.Hour)},
		3103: {Status: "completed", UpdatedAt: clock.Add(24 * time.Hour)},
		3104: {Status: "completed"},
	}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		questCatalog:       catalog,
		gameplayTimers:     &itemGrantNoticeTimerQueue{now: clock},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{connID: "daily-rollover-clear-list", selectedCharacterID: 19}

	transport, err := service.buildCurrentClearQuestListTransportBodyForSession(
		ctx, session, "test_daily_rollover_clear_list")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := zlibDecompress(transport)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain[4+3101]; got != 0 {
		t.Fatalf("stale daily completion 3101 flag=%d, want 0 so the daily quest refreshes", got)
	}
	if got := plain[4+3102]; got != 1 {
		t.Fatalf("non-daily completion 3102 flag=%d, want 1", got)
	}
	// 未来日期（时钟回拨/脏数据）和没有完成日证据的每日完成都不得被投影掉：
	// 否则一次回拨或一条未知完成行就能让已发奖的每日任务重新可接、重复发奖。
	for _, questID := range []int{3103, 3104} {
		if got := plain[4+questID]; got != 1 {
			t.Fatalf("daily completion %d flag=%d, want 1 (no earlier service day evidence)", questID, got)
		}
	}

	// 投影只影响下发内容：持久化行必须保留完成状态（回执与 replay 依据不丢）。
	stored, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest record found=%t err=%v", found, err)
	}
	if state := stored.States[3101]; state.Status != "completed" {
		t.Fatalf("persisted daily completion status=%q, want completed", state.Status)
	}
}
