package quest

import (
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// QuestDailyCompletionDayKey 是完成行 Extra 上记录的“本次完成所属服务日”。
//
// 参考实现把每日完成放进独立的按角色/任务/日分桶台账
// （ServerS4A21: Game/Quests/DailyQuestCompletionCycle.cs 的
// character_daily_reset + character_daily_counters，键
// quest_daily_completed_<id>、period=day），完成标记表只作为“当日投影”下发
// （QuestRepository.ReadClearedFlagValue / LoadClearedFlags / LoadAllFlagEntries）。
// 本仓没有这张台账，但任务行自身的 Extra 已经是持久化列，所以把同一个“服务日”
// 写在这里，读侧就能做同样的跨日投影，不需要新表。
const QuestDailyCompletionDayKey = "quest_daily_completed_day_v1"

// isDailyQuestGrade 判定 PVF grade 是否属于“每日任务”。
//
// daily / daily random / special daily 三档逐字对应参考实现
// QuestData.IsDailyGrade（ServerS4A21-build-reference-20261011，
// DfoServer/GameWorld/QuestData.cs:95-102）。
//
// daily mission 是固定真档 Script.pvf
// （sha256 369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06）
// 里安徒恩每日任务实际使用的 grade：10019 n_quest/anton/anton_limit_01.qst
// 「提取安徒恩的神经细胞」、2683 n_quest/anton/daily_anton_upgrade_01.qst
// 「[安徒恩]高级塔尔坦星人的精髓」、12834 n_quest/anton/anton_singleraid_02.qst
// 「[安徒恩单人模式]吞噬火的武器」。参考实现自己的每日完成循环自测也把 2683
// 当作每日任务（SelfTests/DailyQuestCompletionCycleSelfTest.cs:39-41），
// 所以同一份任务在这两代 PVF 里都必须是每日档。
func isDailyQuestGrade(grade string) bool {
	normalized := normalizeQuestTag(grade)
	switch normalized {
	case "daily", "daily random", "daily mission":
		return true
	default:
		// special daily 在既有 grade 判定里按前缀成族处理
		// （selectableGrade / repeatableGrade 同口径），这里沿用。
		return strings.HasPrefix(normalized, "special daily")
	}
}

// dailyCompletionServiceDay 取一条“已完成”任务行所属的服务日，第二个返回值为
// false 表示这一行没有可用的完成日证据。
//
// 证据来源只有两处，都来自持久化写入点，不额外发明数据：
//  1. 完成结算写下的显式日键 QuestDailyCompletionDayKey（本仓新增，见
//     markDailyQuestCompletion 与 finish_owner.go 的完成分支）；
//  2. 旧存档的完成时间 state.UpdatedAt —— 它由同一个完成结算（以及接取时的
//     ApplyAccept）写库，报障里的 10019 完成行（2026-10-10 / 2026-10-12）
//     只有这一个证据。
//
// 两者都没有的行返回 false，调用方按“不投影”处理。
func dailyCompletionServiceDay(state dnfrepo.QuestState) (int64, bool) {
	if state.Extra != nil {
		if raw := strings.TrimSpace(state.Extra[QuestDailyCompletionDayKey]); raw != "" {
			if day, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return day, true
			}
		}
	}
	if !state.UpdatedAt.IsZero() {
		return premium.ServiceDayID(state.UpdatedAt.UTC()), true
	}
	return 0, false
}

// ProjectDailyQuestRollover 返回 record 的一个只读投影：把确定属于**更早服务日**
// 完成的每日任务从“已完成”集合里拿掉。
//
// 这正是参考实现当日投影的语义（ReadClearedFlagValue 在 IsDailyQuest 且
// !IsClaimedToday 时返回 0；LoadClearedFlags / LoadAllFlagEntries 跳过过期的
// 每日完成）。它只影响读侧投影：调用方拿到的是副本，永远不要写回仓储，
// 持久化行仍保留完成回执与 replay 依据。作用范围只有
// “状态是完成态 + 定义存在 + grade 是每日档”的行；进行中和非每日完成行原样保留。
//
// 只有“有完成日证据且严格早于今天”才算过期：
//   - 未来日期（时钟回拨或脏数据）保持完成，否则一次回拨就会让已发奖的每日
//     任务重新变成可接，重复发奖；
//   - 拿不到完成日的行同样保持完成，宁可少刷新一次，也不凭未知证据放行。
func (c *Catalog) ProjectDailyQuestRollover(record dnfrepo.QuestRecord, now time.Time) dnfrepo.QuestRecord {
	if c == nil {
		return record
	}
	today := premium.ServiceDayID(now.UTC())
	record.States = projectDailyCompletionStates(c, record.States, today)
	record.Progress = projectDailyCompletionStates(c, record.Progress, today)
	return record
}

func projectDailyCompletionStates(c *Catalog, states map[int64]dnfrepo.QuestState, today int64) map[int64]dnfrepo.QuestState {
	if c == nil || len(states) == 0 {
		return states
	}
	var projected map[int64]dnfrepo.QuestState
	for questID, state := range states {
		if questID <= 0 || !isCompletedQuestStatus(state.Status) {
			continue
		}
		definition, known := c.Find(questID)
		if !known || !isDailyQuestGrade(definition.Grade) {
			continue
		}
		day, dated := dailyCompletionServiceDay(state)
		if !dated || day >= today {
			continue
		}
		if projected == nil {
			projected = make(map[int64]dnfrepo.QuestState, len(states))
			for key, value := range states {
				projected[key] = value
			}
		}
		delete(projected, questID)
	}
	if projected == nil {
		return states
	}
	return projected
}

// markDailyQuestCompletion 在每日任务的完成行上写下“本次完成所属服务日”，
// 供跨日投影与“每日一次”判定使用。非每日任务不加任何标记。
func markDailyQuestCompletion(state *dnfrepo.QuestState, definition Definition, now time.Time) {
	if state == nil || !isDailyQuestGrade(definition.Grade) {
		return
	}
	if state.Extra == nil {
		state.Extra = make(map[string]string, 1)
	}
	state.Extra[QuestDailyCompletionDayKey] = strconv.FormatInt(premium.ServiceDayID(now.UTC()), 10)
}
