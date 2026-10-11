package quest

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// P6 · J12-b：主线链路（quest clear 家族）重置。
// P6b：同一个家族重置**再加一个入口** —— 请求的任务就是父任务自己。
//
// 本文件是上游 0.1.8 `quest/linked_chain_reset.go:24-139` 的
// `Catalog.PlanLinkedQuestChainReset` 在 90CN 分支的移植。上游那个文件依赖 6 个本仓
// **不存在**的私有符号，逐条按本仓既有实现适配（不照抄、不新写第二套判定）：
//
//	UP:44  c.managedLinkedQuestChainIDs(parent) -> 本文件同名方法（抽取本仓既有判定）
//	UP:114 managedLinkedQuestChainIDs           -> 同上
//	UP:128 shouldActivateLinkedSubQuest(child)  -> 本仓既有 shouldAutoCompleteNoRewardSubQuest
//	                                               （auto_complete.go:259）+ !HasDependGiveItem
//	UP:136 questClearDefinition                 -> 本文件同名函数（本仓原本内联 4 处）
//	UP:84  applyAcceptPlan                      -> 本文件 applyLinkedQuestChainAcceptPlan
//	UP:96  activeQuestState                     -> 同上（与本仓 owner.go:190-211 同形）
//
// **同源性（关键）**：本仓"链接子任务"的判据原本内联在两处完全相同的代码里
// （catalog.go:997-1020 的 linkedSubQuestAcceptPlans、auto_complete.go:361-380 的
// activateMissingLinkedNoRewardSubQuests）。本文件把那一份抽成
// questClearDefinition + linkedQuestChainChild，并让这两处**改为调用它**，
// 行为逐字节不变（等价断言见 linked_chain_reset_test.go 的
// TestLinkedQuestChainPredicatesMatchLegacyInlineForm）。
//
// 第三处 linked_objective.go:67-98 只共用父任务类型判断（questClearDefinition），它的
// **子任务判据不同**（linkedObjectiveQuest = `NoExperience` && 无奖励 `[sub]`，且不看
// `[depend give item]`，走的是另一条"无奖励子目标"语义），因此**故意不合并**，
// 避免把两条不同判据强行统一成一条。
//
// 与上游的三处**故意偏离**（见交付报告「哪些上游行为我故意没照抄」）：
//
//  1. 上游在"请求的任务行不存在"时 `delete` 是静默空操作，然后照样重新激活一代并
//     报成功 —— 等于凭空给角色激活一条他从没有过的链路。本仓改为**明确失败**
//     （ErrQuestChainRowMissing），对应派工单硬性要求 2「不许静默假成功」。
//  2. 上游只把 `[int data]` 里能被解析的成员算进家族，解析不到的成员被静默跳过 ——
//     于是"只重置看到的那部分"仍报成功。本仓对两种情况显式报错：
//     父任务 `[int data]` 点名了目录里根本不存在的任务 id（ErrQuestChainChildUnresolved），
//     或者家族成员在 reset 后**无法全部重新激活**（ErrQuestChainIncompleteReactivation）。
//  3. 上游对 `requested.MainQuestID <= 0`、父任务不存在、父任务不是 quest clear 家族
//     统一返回 ErrQuestNotAcceptable（无法区分原因）。本仓改为各自的具名哨兵，
//     让面板能把"为什么被拒"如实回报给操作者。
//
// P6b（本文件第二部分）补的是 P6 交付报告 §8-② 自己点名的缺口：quest clear 家族父任务
// 自己的 `[main quest]` 是 0，于是"反完成父任务"从 PlanLinkedQuestChainReset 走会恒返
// ErrQuestChainNotLinked（父任务确实不是"链接子任务"），面板只能退化成"删掉父行"——
// 而同一家族里已持久化的子任务世代会留在库里。
//
// 新增的入口 `PlanLinkedQuestChainResetFromParent` 只把"谁算发起点"从子任务换成父任务：
// **家族判据、删除范围、重建口径与子任务入口共用同一份实现**（managedLinkedQuestChainIDs
// + resetLinkedQuestChainFamily），没有第二套家族判定，也没有第二个"重建回来"的口径。
//
// 与上游的差异：上游 `gm.go:1359` 的判据是 `definition.MainQuestID > 0`，父任务一律走
// "删父行"（上游根本没有"从父任务发起整族重置"这个能力）。本仓**有意偏离**，理由见
// 交付报告 §3：只删父行是半截操作，操作者无从知道子任务世代还留在库里。

// 链路重置的具名拒绝原因（上游统一吞成 ErrQuestNotAcceptable，本仓分开）。
var (
	// ErrQuestChainNotLinked：请求的任务在 PVF 里没有 [main quest]，不属于任何链路。
	ErrQuestChainNotLinked = errors.New("dnf quest is not a linked quest-clear sub-quest")
	// ErrQuestChainParentMissing：请求的任务声明了 [main quest]，但父任务不在任务目录里。
	ErrQuestChainParentMissing = errors.New("dnf quest chain parent definition is missing")
	// ErrQuestChainParentNotQuestClear：父任务存在，但它的 [type] 不是 quest clear 家族。
	ErrQuestChainParentNotQuestClear = errors.New("dnf quest chain parent is not a quest-clear family owner")
	// ErrQuestChainRequestedNotManaged：请求的任务不是该家族里"被管理的链接子任务"
	// （[main quest] 没指回父任务 / 带 [depend give item] / 不是无奖励 [sub] 子任务）。
	ErrQuestChainRequestedNotManaged = errors.New("dnf quest chain requested quest is not a managed linked sub-quest")
	// ErrQuestChainRowMissing：请求的任务在该角色的任务账里根本没有行。
	ErrQuestChainRowMissing = errors.New("dnf quest chain requested quest row is missing")
	// ErrQuestChainChildUnresolved：父任务 [int data] 里点名的子任务在任务目录里不存在
	// —— 链数据不完整，拒绝重置（绝不"只重置看到的那部分"）。
	ErrQuestChainChildUnresolved = errors.New("dnf quest chain references an unresolved child quest id")
	// ErrQuestChainIncompleteReactivation：家族里已有成员无法按 PVF 重新激活
	// （例如子任务的等级窗口/资格已经不接受当前角色），拒绝写入以免留下"删了但没建回来"。
	ErrQuestChainIncompleteReactivation = errors.New("dnf quest chain family cannot be fully reactivated")
	// ErrQuestChainNoManagedChildren（P6b）：请求的任务是 quest clear 父任务，但它的
	// `[int data]` 里**一个被管理的链接子任务都没有**（家族只有父任务自己）——
	// 没有可整族重置的世代可谈，调用方应走普通单条路径；本入口绝不编造家族成员。
	ErrQuestChainNoManagedChildren = errors.New("dnf quest chain parent owns no managed linked sub-quest")
)

// LinkedQuestChainResetRejection 是链路重置的**具名**拒绝：Reason 是哨兵（调用方用
// errors.Is 判定），其余字段是"点名"用的结构化细节，让面板能如实说清是哪一个
// quest id / 哪几个子任务导致拒绝，而不是把原因糊成一句"不合法"。
type LinkedQuestChainResetRejection struct {
	Reason        error
	QuestID       int64
	ParentQuestID int64
	Details       []int64
}

func (e *LinkedQuestChainResetRejection) Error() string {
	return fmt.Sprintf("dnf quest chain reset rejected (%v): quest=%d parent=%d details=%v",
		e.Reason, e.QuestID, e.ParentQuestID, e.Details)
}

// Unwrap 让 errors.Is(err, ErrQuestChainXxx) 继续可用。
func (e *LinkedQuestChainResetRejection) Unwrap() error { return e.Reason }

func chainReject(reason error, questID, parentQuestID int64, details ...int64) error {
	return &LinkedQuestChainResetRejection{
		Reason:        reason,
		QuestID:       questID,
		ParentQuestID: parentQuestID,
		Details:       details,
	}
}

// LinkedQuestChainResetPlan 是一条 quest clear 家族的管理性重置投影。
//
// 链接子任务无法单独被"反完成"：客户端从父任务与**同一世代的所有兄弟行**推导它的
// NPC/对白归属，所以本操作把整族已持久化的世代删掉，再按 PVF 重新激活一代。
// 链外任务一字节都不动。
type LinkedQuestChainResetPlan struct {
	// Record 是重置之后的**新**记录（输入记录不被修改）。
	Record dnfrepo.QuestRecord
	// ParentQuestID 是被重置家族的父任务（quest clear）。
	ParentQuestID int64
	// RemovedQuestIDs 是被删掉的已持久化任务 id（父+子，升序，只含确实有行的）。
	RemovedQuestIDs []int64
	// ReactivatedQuestIDs 是重新激活的一代（父在前，其后是子任务，与 PVF 顺序一致）。
	ReactivatedQuestIDs []int64
	// ChangedFields 与上游同为 States + Progress（两张表都参与删除与写入）。
	ChangedFields []dnfrepo.QuestField
}

// PlanLinkedQuestChainReset 删除一条 linked quest-clear 家族的全部已持久化世代，
// 并按 PVF 重新激活一代（父 active + 被管理的子任务 active，子任务带
// main_quest_id / auto_activated_by_main_quest 标记）。普通独立任务**故意**不在此
// 操作范围内。任何一步不满足都会返回具名错误，调用方必须零写入。
//
// 本入口的**发起点是链接子任务**（`[main quest] > 0`）。请求 quest clear 父任务自己
// （`[main quest] == 0`）时这里是 ErrQuestChainNotLinked —— 那条路径走 P6b 的
// PlanLinkedQuestChainResetFromParent，两者共用 resetLinkedQuestChainFamily。
// 本函数对既有调用方的行为**逐字节不变**（P6 真档用例钉着）。
func (c *Catalog) PlanLinkedQuestChainReset(
	character CharacterEligibility,
	record dnfrepo.QuestRecord,
	requestedQuestID int64,
	resetAt time.Time,
) (LinkedQuestChainResetPlan, error) {
	if c == nil {
		return LinkedQuestChainResetPlan{}, ErrCatalogEmpty
	}
	requested, ok := c.Find(requestedQuestID)
	if !ok {
		return LinkedQuestChainResetPlan{}, ErrQuestDefinitionMissing
	}
	if requested.MainQuestID <= 0 {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainNotLinked, requestedQuestID, 0)
	}
	parent, ok := c.Find(requested.MainQuestID)
	if !ok {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainParentMissing, requestedQuestID, requested.MainQuestID)
	}
	if !questClearDefinition(parent) {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainParentNotQuestClear, requestedQuestID, parent.ID)
	}
	familyIDs := c.managedLinkedQuestChainIDs(parent)
	requestedManaged := false
	for _, questID := range familyIDs[1:] {
		if questID == requestedQuestID {
			requestedManaged = true
			break
		}
	}
	if !requestedManaged {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainRequestedNotManaged, requestedQuestID, parent.ID)
	}
	// 链数据完整性（派工单硬性要求 2 + 计划书 §783）：父任务 [int data] 里点名、
	// 但目录里不存在的子任务 id —— 拒绝，而不是只重置看得见的那部分。
	if unresolved := c.linkedQuestChainUnresolvedChildIDs(parent); len(unresolved) != 0 {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainChildUnresolved, requestedQuestID, parent.ID, unresolved...)
	}
	// 请求的任务必须**确实有行**才谈得上"重置世代"（上游这里是静默空操作）。
	if _, known := questStateFor(record, requestedQuestID); !known {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainRowMissing, requestedQuestID, parent.ID)
	}
	return c.resetLinkedQuestChainFamily(character, record, parent, familyIDs, requestedQuestID, resetAt)
}

// PlanLinkedQuestChainResetFromParent 是"操作者点的就是 quest clear 父任务那一行"的
// 入口（P6b）。除发起点之外，与 PlanLinkedQuestChainReset **共用同一段**家族重置。
//
// 为什么需要它：quest clear 家族父任务自己的 `[main quest]` 是 0（真档 104 个 quest clear
// 根全部如此，见 linked_chain_reset_test.go 的真档断言），因此请求父任务时上面那个入口
// 恒返 ErrQuestChainNotLinked —— 面板只能退化成"删掉父行"，同一家族里已持久化的子任务
// 世代会留在库里，之后重新接取会撞 CollisionQuests / 家族判定（P6 报告 §8-②）。
//
// 判据与另一入口完全同源：家族成员仍由 managedLinkedQuestChainIDs 决定，删除与重建仍走
// resetLinkedQuestChainFamily；这里只多一层"请求者必须**自己就是** quest clear 父任务"。
// 请求的父任务没有任何被管理的链接子任务时返回 ErrQuestChainNoManagedChildren（具名、
// 零写入）：调用方应走普通单条路径 —— 没有家族可重置时绝不编造。
//
// 请求的父任务在该角色的任务账里没有行时返回 ErrQuestChainRowMissing（与子任务入口同
// 口径：上游在"行不存在"时是静默空操作，本仓明确失败）。
func (c *Catalog) PlanLinkedQuestChainResetFromParent(
	character CharacterEligibility,
	record dnfrepo.QuestRecord,
	parentQuestID int64,
	resetAt time.Time,
) (LinkedQuestChainResetPlan, error) {
	if c == nil {
		return LinkedQuestChainResetPlan{}, ErrCatalogEmpty
	}
	parent, ok := c.Find(parentQuestID)
	if !ok {
		return LinkedQuestChainResetPlan{}, ErrQuestDefinitionMissing
	}
	// 请求的其实是链接子任务：交给上面那个**唯一**的子任务入口，两个入口的行为不许分叉。
	if parent.MainQuestID > 0 {
		return c.PlanLinkedQuestChainReset(character, record, parentQuestID, resetAt)
	}
	if !questClearDefinition(parent) {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainNotLinked, parentQuestID, 0)
	}
	familyIDs := c.managedLinkedQuestChainIDs(parent)
	if len(familyIDs) <= 1 {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainNoManagedChildren, parentQuestID, parent.ID)
	}
	// 与子任务入口同一处完整性判据：父任务 [int data] 点名了目录里不存在的任务 ——
	// 家族数据不完整，拒绝，而不是"只重置看得见的那部分"。
	if unresolved := c.linkedQuestChainUnresolvedChildIDs(parent); len(unresolved) != 0 {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainChildUnresolved, parentQuestID, parent.ID, unresolved...)
	}
	if _, known := questStateFor(record, parentQuestID); !known {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainRowMissing, parentQuestID, parent.ID)
	}
	return c.resetLinkedQuestChainFamily(character, record, parent, familyIDs, parentQuestID, resetAt)
}

// ManagedLinkedQuestChainFamily 报告"questID **自己就是**一条 quest clear 家族的父任务、
// 并且至少有一个被管理的链接子任务"，并返回该家族成员（父在前、PVF 顺序）。
//
// P6b 的面板接线用它的判据：只有为真时才把 uncomplete 交给父任务入口做整族重置；为假
// （普通任务、链接子任务、[int data] 里没有受管子任务的 childless 父任务、[int data] 全是
// 悬空引用的父任务）时调用方继续走 P5 的普通单条路径。这里**没有新写的家族逻辑** ——
// 它就是 managedLinkedQuestChainIDs 的一次封装，家族判据仍然只有那一份。
func (c *Catalog) ManagedLinkedQuestChainFamily(questID int64) ([]int64, bool) {
	if c == nil {
		return nil, false
	}
	definition, ok := c.Find(questID)
	if !ok || definition.MainQuestID > 0 || !questClearDefinition(definition) {
		return nil, false
	}
	family := c.managedLinkedQuestChainIDs(definition)
	if len(family) <= 1 {
		return nil, false
	}
	return family, true
}

// resetLinkedQuestChainFamily 是两个入口（请求子任务 / 请求父任务）共用的**同一段**
// 家族重置：删掉整族已持久化的世代，再按 PVF 重新激活一代，并保证整族都激活回来。
//
// requestedQuestID 只用于具名拒绝里"点名是谁发起的请求"；父任务入口传父任务自己。
func (c *Catalog) resetLinkedQuestChainFamily(
	character CharacterEligibility,
	record dnfrepo.QuestRecord,
	parent Definition,
	familyIDs []int64,
	requestedQuestID int64,
	resetAt time.Time,
) (LinkedQuestChainResetPlan, error) {
	reset := dnfrepo.CloneQuest(record)
	removed := make([]int64, 0, len(familyIDs))
	for _, questID := range familyIDs {
		if _, known := questStateFor(reset, questID); known {
			removed = append(removed, questID)
		}
		delete(reset.States, questID)
		delete(reset.Progress, questID)
	}
	accept, err := c.PlanAccept(character, reset, parent.ID)
	if err != nil {
		return LinkedQuestChainResetPlan{}, err
	}
	// 整族必须全部激活回来：PlanAccept 只激活"当前资格允许"的子任务，若某个被管理
	// 子任务激活不回来，就会留下"行已删、代没建回"的半截状态 —— 明确失败。
	reactivated := make([]int64, 0, 1+len(accept.LinkedSubQuests))
	reactivated = append(reactivated, accept.QuestID)
	reactivatedSet := map[int64]struct{}{accept.QuestID: {}}
	for _, linked := range accept.LinkedSubQuests {
		reactivated = append(reactivated, linked.QuestID)
		reactivatedSet[linked.QuestID] = struct{}{}
	}
	missing := make([]int64, 0)
	for _, questID := range familyIDs {
		if _, ok := reactivatedSet[questID]; !ok {
			missing = append(missing, questID)
		}
	}
	if len(missing) != 0 {
		return LinkedQuestChainResetPlan{}, chainReject(ErrQuestChainIncompleteReactivation, requestedQuestID, parent.ID, missing...)
	}
	if resetAt.IsZero() {
		resetAt = time.Now().UTC()
	}
	applyLinkedQuestChainAcceptPlan(&reset, accept, resetAt)
	reset.UpdatedAt = resetAt

	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return LinkedQuestChainResetPlan{
		Record:              reset,
		ParentQuestID:       parent.ID,
		RemovedQuestIDs:     removed,
		ReactivatedQuestIDs: reactivated,
		ChangedFields:       []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress},
	}, nil
}

// applyLinkedQuestChainAcceptPlan 把 PlanAccept 的结果写成"激活的一代"。
//
// 与上游 applyAcceptPlan（UP:84-94）/ activeQuestState（UP:96-111）逐字对应；
// 与本仓 owner.go:190-211（ApplyAccept 真正落库的那段）**同形**，唯一差别是这里
// 会同时删掉 Progress 里的同 id 行 —— 因为链路重置写两张表（上游 ResetPlan 的
// ChangedFields 也是 States + Progress），只删 States 会让残留的 Progress 行继续
// 出现在 op0x23E 的"进行中任务"快照里。
func applyLinkedQuestChainAcceptPlan(record *dnfrepo.QuestRecord, plan AcceptPlan, activatedAt time.Time) {
	if record == nil {
		return
	}
	if record.States == nil {
		record.States = make(map[int64]dnfrepo.QuestState, 1+len(plan.LinkedSubQuests))
	}
	record.States[plan.QuestID] = activeLinkedQuestState(plan.Path, plan.Type, plan.InitTrigger, activatedAt, 0)
	delete(record.Progress, plan.QuestID)
	for _, linked := range plan.LinkedSubQuests {
		record.States[linked.QuestID] = activeLinkedQuestState(linked.Path, linked.Type, linked.InitTrigger, activatedAt, plan.QuestID)
		delete(record.Progress, linked.QuestID)
	}
}

// activeLinkedQuestState 与上游 activeQuestState（UP:96-111）一致：父任务只带
// pvf_path/quest_type，子任务额外带 main_quest_id + auto_activated_by_main_quest
// （键名与本仓 owner.go:205-208 逐字相同，客户端与既有测试都读这两个键）。
func activeLinkedQuestState(path, questType string, trigger uint32, activatedAt time.Time, parentQuestID int64) dnfrepo.QuestState {
	extra := map[string]string{
		"pvf_path":   path,
		"quest_type": questType,
	}
	if parentQuestID > 0 {
		extra["main_quest_id"] = strconv.FormatInt(parentQuestID, 10)
		extra["auto_activated_by_main_quest"] = "true"
	}
	return dnfrepo.QuestState{
		Status:        "active",
		ProgressValue: int64(trigger),
		UpdatedAt:     activatedAt,
		Extra:         extra,
	}
}

// managedLinkedQuestChainIDs 返回被管理的 quest clear 家族：[父任务] + 父任务
// `[int data]` 里点名的、`[main quest]` 指回父任务的、不带 `[depend give item]` 的
// 无奖励 `[sub]` 子任务（去重、保持 PVF 顺序）。
//
// 判据与 linkedSubQuestAcceptPlans / activateMissingLinkedNoRewardSubQuests **同一份**
// （linkedQuestChainChild），因此"能自动激活的子任务"与"链路重置会重建的子任务"
// 永远是同一批，不会出现两套口径。
//
// 与上游 managedLinkedQuestChainIDs（UP:113-134）的差别只有一处：上游先做
// questClearDefinition(parent) 再遍历，这里假定调用方已经验过父任务类型
// （本文件的三个调用点都验过），非 quest clear 的父任务直接返回 [父] 一个成员。
func (c *Catalog) managedLinkedQuestChainIDs(parent Definition) []int64 {
	ids := []int64{parent.ID}
	if c == nil || !questClearDefinition(parent) {
		return ids
	}
	seen := make(map[int64]struct{}, len(parent.IntData))
	for _, childID := range parent.IntData {
		if childID <= 0 {
			continue
		}
		if _, duplicate := seen[childID]; duplicate {
			continue
		}
		seen[childID] = struct{}{}
		child, ok := c.Find(childID)
		if !ok || !linkedQuestChainChild(parent, child) {
			continue
		}
		ids = append(ids, childID)
	}
	return ids
}

// linkedQuestChainUnresolvedChildIDs 返回父任务 `[int data]` 里点名、但在任务目录里
// **根本不存在**的子任务 id（去重、升序）。这是"链数据不完整"的可判定证据：
// 父任务的初始进度（questClearInitialTrigger）会把每一个正数成员都算作"还没完成"，
// 而目录里没有定义的任务不可能被激活 —— 家族永远无法自洽。
func (c *Catalog) linkedQuestChainUnresolvedChildIDs(parent Definition) []int64 {
	if c == nil || !questClearDefinition(parent) {
		return nil
	}
	seen := make(map[int64]struct{}, len(parent.IntData))
	unresolved := make([]int64, 0)
	for _, childID := range parent.IntData {
		if childID <= 0 {
			continue
		}
		if _, duplicate := seen[childID]; duplicate {
			continue
		}
		seen[childID] = struct{}{}
		if _, ok := c.Find(childID); !ok {
			unresolved = append(unresolved, childID)
		}
	}
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i] < unresolved[j] })
	return unresolved
}

// linkedQuestChainChild 是"链接子任务"判定的**唯一**实现，抽取自本仓原本内联在
// catalog.go:1018 与 auto_complete.go:376-378 的同一份表达式：
//
//	child.MainQuestID == parent.ID && !child.HasDependGiveItem && shouldAutoCompleteNoRewardSubQuest(child)
//
// 三项分别对应上游 managedLinkedQuestChainIDs 的三条 continue（UP:128）：
// `[main quest]` 必须指回父任务、带 `[depend give item]` 的子任务不由父任务代管、
// 以及本仓 `shouldActivateLinkedSubQuest` 的等价物 shouldAutoCompleteNoRewardSubQuest
// （auto_complete.go:259-269：`[grade] [sub]` + 无奖励数据 + 奖励解析无错）。
func linkedQuestChainChild(parent Definition, child Definition) bool {
	return child.MainQuestID == parent.ID &&
		!child.HasDependGiveItem &&
		shouldAutoCompleteNoRewardSubQuest(child)
}

// questClearDefinition 等价上游 questClearDefinition（UP:136-139）：`[type]` 归一化后
// 是 `quest clear` 或 `clear quest`。本仓原本把这同一个判断内联在四处
// （catalog.go:997-1000、auto_complete.go:361-364、linked_objective.go:67-68 / 77-80），
// 这里收敛成一处，四处已全部改为调用它。
func questClearDefinition(definition Definition) bool {
	tag := normalizeQuestTag(definition.Type)
	return tag == "quest clear" || tag == "clear quest"
}
