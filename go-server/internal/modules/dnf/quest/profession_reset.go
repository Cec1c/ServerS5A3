package quest

import (
	"sort"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6 · J12-a：职业任务账的基准态重置。
//
// 本文件是上游 0.1.8 `quest/profession_reset.go:23-64` 的
// `Catalog.PlanBaseProfessionQuestReset` 在 90CN 分支的移植，逐段对应：
//
//	UP:23  PlanBaseProfessionQuestReset         -> 本文件同名方法
//	UP:29  resetProfessionQuestProjection       -> 本文件同名函数
//	UP:24  c == nil -> ErrCatalogEmpty          -> 本仓同哨兵（catalog.go:20）
//	UP:27  dnfrepo.CloneQuest                   -> 本仓同名（repository 包）
//
// 为什么需要它：职业任务的任务账**独立于**角色的 `grow_type` 存储。只改角色成长值
// （或只写一行 GM 完成态）会把"第一职业 / 第一次觉醒 / 第二次觉醒"的旧任务行留在
// 任务账里 —— 客户端 0x164 的"已完成任务"位图按 quest id 索引、op21 可接任务列表按
// PVF 重新算，两者与角色当前成长段就会互相矛盾（与 gmtool/profession_quests.go:13-24
// 记录的同一类现象）。
//
// 三条与上游逐字相同的口径：
//
//  1. **只碰 `JobChangeQuest ∈ 1..3`**（第一职业 / 一次觉醒 / 二次觉醒）。
//     专家职业（`JobChangeQuest == 20` 那一族，真档实测 2702/2708/2710/2712/
//     10020/10021/11007/... 共 15 个）**故意不动**：它们的完成态是另一条成长线，
//     不在"退回基础职业"的范围内。
//  2. **plan / apply 分离**：本函数只读输入记录并返回投影，绝不写仓储。
//     apply 由调用方在事务内做（gmtool 侧见 api_quest_mutations.go 的职业分支，
//     本仓同款设计见 gmtool/profession_quests.go:78-166 的 planProfessionQuestStates）。
//  3. **无变化时不动 `UpdatedAt`**：没有任何职业任务行被删掉时，plan.Record 与原记录
//     逐字节相同（含 UpdatedAt），ChangedFields 为空 —— 调用方据此跳过写入。
type ProfessionQuestResetPlan struct {
	// Record 是删除职业任务行之后的**新**记录（输入记录不被修改）。
	Record dnfrepo.QuestRecord
	// ResetQuestIDs 是被删掉的任务 id，升序、去重（States 与 Progress 合并计数一次）。
	ResetQuestIDs []int64
	// ChangedFields 是真正发生变化的投影；两者都没变时为空切片。
	ChangedFields []dnfrepo.QuestField
}

// PlanBaseProfessionQuestReset 删除任务账里所有"第一职业 / 第一次觉醒 / 第二次觉醒"
// 任务行（`JobChangeQuest ∈ 1..3`，以当前运行时 PVF 的判定为准），专家职业与普通任务
// 一行不动。返回的 plan 与 apply 分离，见上面文件头注释。
func (c *Catalog) PlanBaseProfessionQuestReset(record dnfrepo.QuestRecord, resetAt time.Time) (ProfessionQuestResetPlan, error) {
	if c == nil {
		return ProfessionQuestResetPlan{}, ErrCatalogEmpty
	}
	reset := dnfrepo.CloneQuest(record)
	resetIDs := make(map[int64]struct{})
	// 两张投影表各扫一遍：同一个 id 可能同时存在于 States 与 Progress（老写路径 /
	// 迁移数据），resetIDs 是集合，所以两侧命中只报告一次。
	statesChanged := resetProfessionQuestProjection(c, reset.States, resetIDs)
	progressChanged := resetProfessionQuestProjection(c, reset.Progress, resetIDs)
	ids := make([]int64, 0, len(resetIDs))
	for questID := range resetIDs {
		ids = append(ids, questID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fields := make([]dnfrepo.QuestField, 0, 2)
	if statesChanged {
		fields = append(fields, dnfrepo.QuestFieldStates)
	}
	if progressChanged {
		fields = append(fields, dnfrepo.QuestFieldProgress)
	}
	if len(fields) != 0 {
		if resetAt.IsZero() {
			resetAt = time.Now().UTC()
		}
		reset.UpdatedAt = resetAt
	}
	return ProfessionQuestResetPlan{Record: reset, ResetQuestIDs: ids, ChangedFields: fields}, nil
}

// resetProfessionQuestProjection 从一张投影表里删掉职业任务行，并返回是否有变化。
// 目录里找不到定义的任务**不删**（不猜、不误伤）：判定完全依赖 PVF 的
// `[job change quest]`，而不是任务 id 区段或任务类型。
func resetProfessionQuestProjection(c *Catalog, states map[int64]dnfrepo.QuestState, resetIDs map[int64]struct{}) bool {
	changed := false
	for questID := range states {
		definition, ok := c.Find(questID)
		if !ok || definition.JobChangeQuest < 1 || definition.JobChangeQuest > 3 {
			continue
		}
		delete(states, questID)
		resetIDs[questID] = struct{}{}
		changed = true
	}
	return changed
}
