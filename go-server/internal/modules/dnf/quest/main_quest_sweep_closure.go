package quest

import (
	"sort"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// mainQuestSweepMaxDepth bounds the recursive `[main quest]` walk. 真档
// Script.pvf 的 [main quest] 关系是一棵树，深度为 1（父 [epic] → 子 [sub]）；
// 上限只是"绝不因为一条畸形 PVF 把闭包无限展开"的保险丝，正常路径永远在第
// 一层就结束。visited 集合本身已经保证终止（每个父节点只展开一次）。
const mainQuestSweepMaxDepth = 4

// mainQuestSweepSubClosure 返回"必须与被清掉的主线任务一起完成"的 `[sub]` 任务。
//
// 边的定义严格只有一条：PVF 的 `[main quest]`。给定被扫中的父任务集合，
// 凡是 `[main quest]` 指向集合内某个任务的 `[sub]` 任务都属于闭包，并递归
// 展开（子任务自己也是别的 sub 的 main quest 时）。
//
// 为什么必须是这条边而不是别的：
//
//   - `[int data]` 只是 `[quest clear]` 父任务自己的子任务清单，跟随它会把
//     兄弟分支也拖进来（真档 3249 的 int data 与 [main quest] 反向索引在
//     普通剧情上一致，但没有 PVF 保证二者永远一致）；
//   - `[pre required quest]` 是剧情先后顺序，不是从属关系，跟随它等于把整条
//     主线后面还没到等级的内容也标完成。
//
// 因此本函数只按 `[main quest]` 反向索引走，且只吞 grade 为 `[sub]` 的行：
// 另一个 `[epic]` 任务即使写了 `[main quest]`，也不由"父任务被扫中"来决定它的
// 完成状态（它有自己的等级/资格判据，由主循环负责）。
//
// 证据（真档 Script.pvf，2026-09-25）：3971 `timebeast_epic_25.qst`
// `[epic] [quest clear] [int data] 3972 3973`，而 3972/3973
// （`timebeast_epic_25_01.qst` / `_02.qst`）的 `[grade]` 都是 `[sub]`、
// `[main quest]` 都是 3971。完成券只扫 `[epic]`，所以 3971 被完成而 3972/3973
// 没有被完成，客户端于是长期保留一个"父已完成、子仍可接取"的死节点。
func (c *Catalog) mainQuestSweepSubClosure(parents map[int64]struct{}, include ...func(Definition) bool) []int64 {
	if c == nil || len(parents) == 0 {
		return nil
	}
	children := make(map[int64][]int64, len(parents))
	for _, definition := range c.ordered {
		if definition.MainQuestID <= 0 || normalizeQuestTag(definition.Grade) != "sub" || (len(include) > 0 && !include[0](definition)) {
			continue
		}
		children[definition.MainQuestID] = append(children[definition.MainQuestID], definition.ID)
	}
	if len(children) == 0 {
		return nil
	}

	frontier := make([]int64, 0, len(parents))
	for parentID := range parents {
		frontier = append(frontier, parentID)
	}
	sort.Slice(frontier, func(left, right int) bool { return frontier[left] < frontier[right] })

	expanded := make(map[int64]struct{}, len(parents))
	closed := make(map[int64]struct{})
	closure := make([]int64, 0)
	for depth := 0; depth < mainQuestSweepMaxDepth && len(frontier) > 0; depth++ {
		next := make([]int64, 0)
		for _, parentID := range frontier {
			if _, seen := expanded[parentID]; seen {
				continue
			}
			expanded[parentID] = struct{}{}
			kids := children[parentID]
			sort.Slice(kids, func(left, right int) bool { return kids[left] < kids[right] })
			for _, childID := range kids {
				if _, already := closed[childID]; already {
					continue
				}
				closed[childID] = struct{}{}
				closure = append(closure, childID)
				next = append(next, childID)
			}
		}
		frontier = next
	}
	if len(closure) == 0 {
		return nil
	}
	sort.Slice(closure, func(left, right int) bool { return closure[left] < closure[right] })
	return closure
}

// mainQuestSweepSubQuestState 写一条"因为父任务被清掉而一并完成"的 `[sub]` 行。
//
// 与 couponMainQuestClearState 同一条口径：只写 Status / ProgressValue / Extra，
// 不发经验、金币、物品，也不写结算回执；reward_state=granted 沿用既有的
// "这条任务永远不会再发奖励"标记，因此子任务奖励不会被二次结算。
// 额外的 main_quest_id / auto_complete_reason 让这类行可以被单独审计出来。
func mainQuestSweepSubQuestState(
	state dnfrepo.QuestState,
	definition Definition,
	parentID int64,
	cutoffLevel int,
	completedAt time.Time,
) dnfrepo.QuestState {
	state.Status = "completed"
	state.ProgressValue = 0
	state.RewardSelectIndex = 0
	state.UpdatedAt = completedAt
	if state.Extra == nil {
		state.Extra = make(map[string]string, 8)
	}
	if state.Extra["pvf_path"] == "" {
		state.Extra["pvf_path"] = definition.Path
	}
	if state.Extra["quest_type"] == "" {
		state.Extra["quest_type"] = definition.Type
	}
	state.Extra["reward_state"] = finishRewardGranted
	state.Extra["auto_completed"] = "true"
	state.Extra["auto_complete_reason"] = "main_quest_swept_sub_closure_no_reward"
	state.Extra["main_quest_id"] = strconv.FormatInt(parentID, 10)
	state.Extra["auto_complete_level_cutoff"] = strconv.Itoa(cutoffLevel)
	return state
}

// applyMainQuestSweepSubClosure 把闭包行写进计划，并返回闭包里最大的任务 id
// （调用方只在"这一轮没有任何父任务被改动"时用它兜底 HighestCompletedQuest）。
//
// 幂等：已经是 completed 的行一个字节都不改；已经 active 的行按同一条无奖励
// 口径关闭（父任务已经完成，这条子任务不可能再通过父任务被激活）。
func (c *Catalog) applyMainQuestSweepSubClosure(
	plan *AutoCompleteMainPlan,
	parents map[int64]struct{},
	cutoffLevel int,
	completedAt time.Time,
	include ...func(Definition) bool,
) int64 {
	if plan == nil || len(parents) == 0 {
		return 0
	}
	maxSubID := int64(0)
	for _, subID := range c.mainQuestSweepSubClosure(parents, include...) {
		definition, known := c.Find(subID)
		if !known {
			continue
		}
		state, field, stateKnown := mutableQuestState(&plan.Record, subID)
		if stateKnown && isCompletedQuestStatus(state.Status) {
			continue
		}
		if !stateKnown {
			field = dnfrepo.QuestFieldStates
			state = dnfrepo.QuestState{Extra: map[string]string{
				"pvf_path":   definition.Path,
				"quest_type": definition.Type,
			}}
		}
		state = mainQuestSweepSubQuestState(state, definition, definition.MainQuestID, cutoffLevel, completedAt)
		switch field {
		case dnfrepo.QuestFieldStates:
			if plan.Record.States == nil {
				plan.Record.States = make(map[int64]dnfrepo.QuestState, 1)
			}
			plan.Record.States[subID] = state
		case dnfrepo.QuestFieldProgress:
			if plan.Record.Progress == nil {
				plan.Record.Progress = make(map[int64]dnfrepo.QuestState, 1)
			}
			plan.Record.Progress[subID] = state
		}
		plan.ChangedLinkedSubIDs = append(plan.ChangedLinkedSubIDs, subID)
		plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
		if subID > maxSubID {
			maxSubID = subID
		}
	}
	sort.Slice(plan.ChangedLinkedSubIDs, func(left, right int) bool {
		return plan.ChangedLinkedSubIDs[left] < plan.ChangedLinkedSubIDs[right]
	})
	return maxSubID
}
