package gmtool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// GM 面板「设置成长/觉醒」只写 grow_type 会留下一个自相矛盾的角色：客户端
// 判断「转职/觉醒是否已完成」用的是两处 PVF + 角色任务状态：
//
//   - S2C 0x164（当前 EXE sub_1D58470）的 30000 字节「已完成任务」位图：按
//     quest id 索引，只有仓储里存在 completed 行才会置 1，客户端据此刷新任务
//     与 NPC 可见性；
//   - S2C 0x15（op21 可接任务列表）：dnfbridge 用 quest.Catalog.QuestList 把
//     PVF 里仍然可接的转职/觉醒任务发给客户端。
//
// 所以覆写 grow_type 时必须一并把该职业/成长段对应的转职与觉醒任务写成
// completed（见 quest.Catalog.ProfessionProgressionQuestIDs），否则任务手册
// 与 NPC 依旧显示「转职 / 觉醒」任务（操作者现象：覆写后转职任务还在）。

// professionQuestOverrideMarker 记录「该完成态由 GM 设置成长写入，不是玩家真
// 的完成了任务」。只落在仓储 Extra 里，不进入任何客户端封包字段。
const professionQuestOverrideMarker = "gm_set_grow_type"

// professionQuestCompletedStatus 与 modules/dnf/quest 的 isCompletedQuestStatus
// 同一口径（双方都读同一份字符串状态）。
const professionQuestCompletedStatus = "completed"

type professionQuestSyncPlan struct {
	TargetIDs   []int64
	Created     []int64
	Completed   []int64
	AlreadyDone []int64
	StaleHigher []int64
	changed     bool
	savedFields []dnfrepo.QuestField
}

func newProfessionQuestSyncPlan() professionQuestSyncPlan {
	return professionQuestSyncPlan{
		TargetIDs:   make([]int64, 0, 16),
		Created:     make([]int64, 0, 16),
		Completed:   make([]int64, 0, 16),
		AlreadyDone: make([]int64, 0, 16),
		StaleHigher: make([]int64, 0, 4),
	}
}

// payload 是回给面板的字段，让操作者能核对到底改了哪些任务行。
func (p professionQuestSyncPlan) payload() map[string]any {
	return map[string]any{
		"targetIds":           p.TargetIDs,
		"createdIds":          p.Created,
		"completedIds":        p.Completed,
		"alreadyCompletedIds": p.AlreadyDone,
		"staleHigherStageIds": p.StaleHigher,
		"unchanged":           !p.changed,
	}
}

// planProfessionQuestStates 计算本次覆写对任务仓储的全部改动，不触碰仓储。
//
// 本函数是「把 PVF 授权任务标成完成」的**唯一**实现，「设置成长/觉醒」与
// 「开启特殊装备槽」（equipment_slots.go）两个入口都走它，避免第二套口径。
//
// 口径：
//   - 已存在的行（States 或 Progress 任一侧）就地翻成 completed，保留 Extra
//     里的完成回执/奖励状态，绝不重写奖励；
//   - 缺失的行按需补一行 completed，带 gm_override 标记：客户端任务位图按 quest id
//     索引，没有行就仍是 0，任务/槽位在客户端不会真的生效；
//   - 不删除任何行：把成长从二次觉醒改回低段位时，属于旧段位的完成行只通过
//     staleHigherStageIds 报告，交由操作者决定。
func planProfessionQuestStates(
	record dnfrepo.QuestRecord,
	targetIDs []int64,
	previousIDs []int64,
	now time.Time,
) (dnfrepo.QuestRecord, professionQuestSyncPlan) {
	updated := dnfrepo.CloneQuest(record)
	plan := newProfessionQuestSyncPlan()
	plan.TargetIDs = append(plan.TargetIDs, targetIDs...)
	sort.Slice(plan.TargetIDs, func(left, right int) bool { return plan.TargetIDs[left] < plan.TargetIDs[right] })

	savedStates, savedProgress := false, false
	for _, questID := range plan.TargetIDs {
		if questID <= 0 {
			continue
		}
		statesRow, inStates := updated.States[questID]
		progressRow, inProgress := updated.Progress[questID]
		switch {
		case !inStates && !inProgress:
			if updated.States == nil {
				updated.States = make(map[int64]dnfrepo.QuestState)
			}
			updated.States[questID] = dnfrepo.QuestState{
				Status:        professionQuestCompletedStatus,
				ProgressValue: 0,
				UpdatedAt:     now,
				Extra:         map[string]string{"gm_override": professionQuestOverrideMarker},
			}
			savedStates = true
			plan.Created = append(plan.Created, questID)
		default:
			// 同一任务可能同时存在 States 与 Progress 两行（迁移数据/旧写路径）。
			// 只改 States 会让残留的 active Progress 行继续出现在 op0x23E 的
			// 进行中任务快照里，所以两侧只要存在就都翻成 completed。
			rowChanged := false
			if inStates {
				if next, flipped := completedQuestStateRow(statesRow, now); flipped {
					updated.States[questID] = next
					savedStates = true
					rowChanged = true
				}
			}
			if inProgress {
				if next, flipped := completedQuestStateRow(progressRow, now); flipped {
					updated.Progress[questID] = next
					savedProgress = true
					rowChanged = true
				}
			}
			if rowChanged {
				plan.Completed = append(plan.Completed, questID)
			} else {
				plan.AlreadyDone = append(plan.AlreadyDone, questID)
			}
		}
	}
	plan.changed = savedStates || savedProgress
	if savedStates {
		plan.savedFields = append(plan.savedFields, dnfrepo.QuestFieldStates)
	}
	if savedProgress {
		plan.savedFields = append(plan.savedFields, dnfrepo.QuestFieldProgress)
	}

	// 只报告：旧成长段位里已经完成、但不属于新段位的行（例如从二次觉醒改回
	// 一次觉醒）。绝不删除玩家的既有完成记录。
	target := make(map[int64]struct{}, len(plan.TargetIDs))
	for _, questID := range plan.TargetIDs {
		target[questID] = struct{}{}
	}
	stale := make(map[int64]struct{}, 4)
	for _, questID := range previousIDs {
		if questID <= 0 {
			continue
		}
		if _, inTarget := target[questID]; inTarget {
			continue
		}
		if questRowCompleted(record, questID) {
			stale[questID] = struct{}{}
		}
	}
	for questID := range stale {
		plan.StaleHigher = append(plan.StaleHigher, questID)
	}
	sort.Slice(plan.StaleHigher, func(left, right int) bool { return plan.StaleHigher[left] < plan.StaleHigher[right] })
	return updated, plan
}

// completedQuestStateRow 把一行任务改成完成态；已经是完成态时返回 false，使
// 重复点击成为空操作（幂等）。Extra 原样保留：不伪造奖励、不写结算回执。
func completedQuestStateRow(state dnfrepo.QuestState, now time.Time) (dnfrepo.QuestState, bool) {
	if questStatusCompleted(state.Status) {
		return state, false
	}
	state.Status = professionQuestCompletedStatus
	state.ProgressValue = 0
	state.UpdatedAt = now
	return state, true
}

func questStatusCompleted(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "complete", "completed", "cleared", "finished", "done":
		return true
	default:
		return false
	}
}

func questRowCompleted(record dnfrepo.QuestRecord, questID int64) bool {
	if state, found := record.States[questID]; found && questStatusCompleted(state.Status) {
		return true
	}
	if state, found := record.Progress[questID]; found && questStatusCompleted(state.Status) {
		return true
	}
	return false
}

// IsProfessionQuestReward 等价上游 gmProfessionRewardType（gm.go:2095-2099）：
// `[reward type]` 归一化之后只认 `grow type` / `awakening type` 两个字面，
// **绝不靠 job id、任务类型或名字猜**（派工单硬性要求 5：不许凭空发明数据）。
//
// 导出它的原因（计划书 §4.5 的 P5 文件清单）：任务写面要用同一处判定
//   - `/complete` 撞到职业任务时的默认拒绝门（P5，默认关）；
//   - `/complete-batch` 的职业任务排除（与上游 gm_extended.go:857-859 同口径）。
//
// P1 的只读面（api_quests.go 的 isProfessionRewardType）也委托到这里，
// 保证读面与写面永远是同一张表、同一个归一化。
func IsProfessionQuestReward(value string) bool {
	switch normalizeQuestRewardTag(value) {
	case "grow type", "awakening type":
		return true
	default:
		return false
	}
}

// syncProfessionQuestStates 把计划写回 dnf_quest_states / dnf_quest_progress。
// 没有任何变化时不写库（重复点击 = 无写入）。targetIDs 为空（例如把成长改回
// 转职前）时依然会读一次记录，只为报告 staleHigherStageIds，绝不删行。
func (s *Server) syncProfessionQuestStates(
	ctx context.Context,
	group dnfrepo.Group,
	characterID string,
	targetIDs []int64,
	previousIDs []int64,
) (professionQuestSyncPlan, error) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		plan := newProfessionQuestSyncPlan()
		plan.TargetIDs = append(plan.TargetIDs, targetIDs...)
		return plan, nil
	}
	if group.Quest == nil {
		return professionQuestSyncPlan{}, fmt.Errorf("任务仓储不可用")
	}
	now := time.Now().UTC()
	record, found, err := group.Quest.Load(ctx, characterID)
	if err != nil {
		return professionQuestSyncPlan{}, fmt.Errorf("读取任务状态失败: %w", err)
	}
	if !found {
		record = dnfrepo.QuestRecord{CharacterID: characterID}
	}
	updated, plan := planProfessionQuestStates(record, targetIDs, previousIDs, now)
	if !plan.changed {
		return plan, nil
	}
	updated.CharacterID = characterID
	updated.UpdatedAt = now
	if err := dnfrepo.SaveQuestFields(ctx, group.Quest, updated, plan.savedFields...); err != nil {
		return plan, fmt.Errorf("写入任务状态失败: %w", err)
	}
	return plan, nil
}
