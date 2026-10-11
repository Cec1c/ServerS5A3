package quest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// CouponMainQuestClearMaxLevel 是运行时 PVF 里所有"按等级完成"的任务完成券能覆盖到的
// 最高主线任务等级。
//
// 证据（真档 `runtime/data/dnf/Script.pvf` 全量 2996 条 quest、698 条 `[epic]`）：
// `[epic]` 的 `[level]` 上限就是 **89**——没有任何一条主线任务声明 Lv90。
// 因此 cutoff 取 89 与取 90 选中的集合逐条相同，89 是两者中更严的一侧。
//
// 客户端证据：右键 `Lv1~90主线任务完成券`（item 10164630、
// `stackable/10164001/10164630.stk`、`[action type] [gm quest clear]`）时，
// 客户端发 op176 GMDEBUG_COMMAND body = u32(12) + `//lvclear 90`
// （2026-09-25 03:38:26 香港封包日志），即它要求"清到 90"；89 与它等价。
// CouponMainQuestClearMaxLevel 是完成券的上限，直接取券的 PVF 说明里的 90：
// 「完成Lv1至Lv90的主线任务」+ `[minimum level] 90`。也就是说 Lv90 角色用券时
// `[level]` 起始 ≤ 90 的主线全部在范围内（含 88 99 / 90 99 这类"当前版本史诗"）。
//
// 任务手册的"任务完成"按钮（op1429）**不共用这个常量**：它的确认框文案是客户端
// 按"等级-1"算出来的（Lv90 角色显示 Lv89），因此仍走
// dnfbridge.currentAutoCompleteMainQuestMaxLevel = 89 与 min(等级-1, 89) 的口径。
// 两条入口共用同一个 planner（PlanCouponMainQuestClear），只有 cutoffLevel 参数
// 不同；日志里的 clear_scope / cutoff_level / include_sub_quests 记录了这层差异。
const CouponMainQuestClearMaxLevel = 90

var (
	ErrCouponClearUnavailable  = errors.New("quest completion coupon is unavailable")
	ErrCouponClearLevelInvalid = errors.New("quest completion coupon cutoff level is invalid")
)

// CouponClearCutoff 把角色等级折算成完成券的清任务等级上限：
// min(角色等级, 90)。
//
// 与任务手册的 op1429 不同（那边是 min(角色等级-1, 89)，依据是客户端确认框
// 文案），完成券的上限按券自己的 PVF 说明取 90：客户端右键券时发的就是
// op176 `//lvclear 90`（2026-09-25 03:38:26 香港封包日志），券的说明也是
// 「完成Lv1至Lv90的主线任务」。取 min 是防御性的：低等级角色即使绕过客户端
// 门禁拿到券，也不会清掉自己等级以上的主线。
// 角色等级 <= 1 时返回 0，调用方必须按无效处理。
func CouponClearCutoff(characterLevel int) int {
	if characterLevel <= 1 {
		return 0
	}
	cutoff := characterLevel
	if cutoff > CouponMainQuestClearMaxLevel {
		cutoff = CouponMainQuestClearMaxLevel
	}
	return cutoff
}

// CouponClearScope 决定"完成券到底完成哪些主线任务"。
//
// 这条区分是本轮取证的核心结论：券的 PVF 说明与客户端提示描述的是**整段等级区间**
// （`完成Lv1至Lv90的主线任务`、客户端补发的 `//lvclear 90`），而任务手册 op1429
// 批量动作此前的口径是"只收当前处于激活态的主线任务"。两者在玩家只接了 1 条
// 主线时会给出完全不同的结果——这正是"提示会完成 Lv89 的任务，实际只完成了当前
// 这一条"的来源。
//
// 现场证据（2026-09-25 03:44 香港日志，操作者会话 game-000007 连点两次任务完成）：
//
//	press1 game-auto-complete-main-quest-success-send request_selector=0
//	       targeted_active_epic_count=1 changed_epic_count=1 highest_completed_quest=3152
//	press2 game-auto-complete-main-quest-single-target-required request_selector=0
//	       reason=main quest auto-complete target is invalid quest_mutation=none
//
// 即服务端并没有"少做"：角色当时只有 1 条 ≤Lv89 的激活主线，所以"完成所有激活主线"
// 恰好等于"完成当前这一条"。
type CouponClearScope string

const (
	// CouponClearScopeActiveOnly 只完成当前处于激活态的主线任务。任务手册的
	// op1429 批量动作从 2026-09-25 起仍是这条口径。
	CouponClearScopeActiveOnly CouponClearScope = "active_only"
	// CouponClearScopeMainStorySweep 完成 cutoff 及以下的**整段主线**：无论角色
	// 是否接过，都按运行时 PVF 的 `[epic]` + `[level]` + 角色/职业/成长谓词落一行
	// completed。这是完成券 PVF 说明的字面语义。
	//
	// 安全性判据（真档 Script.pvf 全量断言，见 coupon_clear_real_pvf_test.go）：
	// 该集合对角色是**闭合**的——集合内每条任务的 `[pre required quest]` 至少有一组
	// 完全落在集合内，且不存在指向集合外非主线任务的引用。因此整段标记完成后不会
	// 出现"已完成的任务其前置未完成"的不一致。
	CouponClearScopeMainStorySweep CouponClearScope = "main_story_sweep"
)

// CouponClearInput 是一次"任务完成券"使用的输入。Eligibility 必须来自角色仓储
// 的最新记录；本模块不接受调用方自报的职业/成长阶段。
type CouponClearInput struct {
	CharacterID string
	Eligibility CharacterEligibility
	CutoffLevel int
	// Scope 留空等价于 CouponClearScopeMainStorySweep（完成券的字面语义）。
	Scope     CouponClearScope
	ClearedAt time.Time
}

// CouponClearResult 是一次完成券结算的结果。它只描述任务行，不携带任何奖励、
// 经验、金币或物品口径——完成券按说明"无任务奖励"。
type CouponClearResult struct {
	CharacterID           string
	CutoffLevel           int
	Scope                 CouponClearScope
	EligibleQuestIDs      []int64
	ChangedQuestIDs       []int64
	ChangedLinkedSubIDs   []int64
	HighestCompletedQuest int64
	ChangedFields         []dnfrepo.QuestField
	PostCommitQuest       dnfrepo.QuestRecord
	// IncludeSubQuests 恒为 true：完成券与"任务完成"按钮都会把被扫中父任务的
	// `[sub]` 子任务一并完成（见 mainQuestSweepSubClosure）。
	IncludeSubQuests bool
	// Idempotent 为 true 表示这次请求没有改动任何任务行（重复使用同一张券、
	// 或角色本来就没有可完成的主线任务）。
	Idempotent bool
}

// PlanCouponMainQuestClear 规划一次"完成券清主线任务"：把 PVF `[level]` 不高于
// cutoffLevel 的主线任务（`[epic]`）标记为完成。
//
// 与任务手册的 op1429 批量动作共用同一条资格规则
// （mainStoryQuestAutoClearable）与同一套落库字段（Status / ProgressValue /
// Extra），差别只有两处：
//
//   - scope：ActiveOnly 只收当前激活的主线（op1429 今天的行为），
//     MainStorySweep 收整段等级区间内的主线（完成券 PVF 说明的语义）；
//   - Extra 里的诊断标记带 "quest_completion_coupon"。
//
// 无论哪种 scope 都：
//
//   - 不发放任何任务奖励（Extra["reward_state"] 直接写成 granted，
//     后续 finish 路径不会再结算一次）；
//   - 不写经验/金币/物品，也不写结算回执；
//   - 找不到任何可完成任务时返回空计划而不是错误——完成券在这种情况下
//     是幂等空操作，调用方据此决定不消耗道具。
func (c *Catalog) PlanCouponMainQuestClear(
	record dnfrepo.QuestRecord,
	character CharacterEligibility,
	cutoffLevel int,
	scope CouponClearScope,
	clearedAt time.Time,
) (AutoCompleteMainPlan, error) {
	if c == nil {
		return AutoCompleteMainPlan{}, ErrCatalogEmpty
	}
	if cutoffLevel <= 0 || cutoffLevel > character.Level || !definitionCharacterJobValid(character) {
		return AutoCompleteMainPlan{}, ErrCouponClearLevelInvalid
	}
	if clearedAt.IsZero() {
		return AutoCompleteMainPlan{}, ErrCouponClearUnavailable
	}
	if scope == "" {
		scope = CouponClearScopeMainStorySweep
	}

	plan := AutoCompleteMainPlan{Record: dnfrepo.CloneQuest(record)}
	sweep := scope == CouponClearScopeMainStorySweep
	// sweptParents 是 `[sub]` 闭包的种子。sweep 口径下它是"所有符合条件的主线"
	// （包括本次调用之前就已经是 completed 的行），因此一张被旧构建写成
	// "父已完成、子还挂着"的记录，下一次用券/点按钮就会被自动补齐；active_only
	// 口径下只收本次真正改动的父任务，DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP=0 的
	// 回退语义保持不变。
	sweptParents := make(map[int64]struct{})
	for _, definition := range c.ordered {
		if !mainStoryQuestAutoClearable(definition, character, cutoffLevel) {
			continue
		}
		if sweep {
			sweptParents[definition.ID] = struct{}{}
		}
		state, field, stateKnown := mutableQuestState(&plan.Record, definition.ID)
		if stateKnown && isCompletedQuestStatus(state.Status) {
			// 已经完成的行一个字节都不改（幂等）。
			continue
		}
		if !(stateKnown && isActiveQuestStatus(state.Status)) {
			// 非激活态（含"角色从没接过、记录里没有这一行"）。
			if !sweep {
				// ActiveOnly：任务手册的批量动作不替玩家"制造"没接过的任务行，
				// 也不动放弃/失败的行。
				continue
			}
			if !stateKnown {
				// Sweep：按需创建 completed 行。只写 Status/ProgressValue/Extra，
				// 不复制任何奖励/进度来源字段（与第七批 GM 覆写同一条口径）。
				field = dnfrepo.QuestFieldStates
				state = dnfrepo.QuestState{
					Extra: map[string]string{
						"pvf_path":   definition.Path,
						"quest_type": definition.Type,
					},
				}
			}
		}
		state = couponMainQuestClearState(state, definition, cutoffLevel, clearedAt)
		switch field {
		case dnfrepo.QuestFieldStates:
			if plan.Record.States == nil {
				plan.Record.States = make(map[int64]dnfrepo.QuestState, 1)
			}
			plan.Record.States[definition.ID] = state
		case dnfrepo.QuestFieldProgress:
			plan.Record.Progress[definition.ID] = state
		}
		if !sweep {
			sweptParents[definition.ID] = struct{}{}
		}
		plan.EligibleQuestIDs = append(plan.EligibleQuestIDs, definition.ID)
		plan.ChangedQuestIDs = append(plan.ChangedQuestIDs, definition.ID)
		plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
		plan.ActiveCompletedCount++
		if definition.ID > plan.HighestCompletedQuest {
			plan.HighestCompletedQuest = definition.ID
		}
	}
	// `[sub]` 闭包：父任务被清掉时，`[main quest]` 指向它的 `[sub]` 子任务必须
	// 一并完成，否则客户端会保留一个"父已完成、子仍可接取"的死节点——2026-09-25
	// char3 的 3971「离别时刻」+ 3972/3973 就是这个形态（券只扫 `[epic]`，
	// 子任务是 `[sub]`）。闭包同样不发奖励、不写结算回执。
	maxSubQuestID := c.applyMainQuestSweepSubClosure(&plan, sweptParents, cutoffLevel, clearedAt)
	if plan.HighestCompletedQuest == 0 && maxSubQuestID > 0 {
		// 只有闭包有活干时（旧记录的自愈），回包仍需要一个非零的最高任务 id。
		plan.HighestCompletedQuest = maxSubQuestID
	}
	if len(plan.ChangedFields) != 0 {
		plan.Record.UpdatedAt = clearedAt
	}
	return plan, nil
}

// ApplyCouponMainQuestClear 在一个角色结算事务里落库完成券的任务行改动。
//
// fail-closed：目录为空、结算事务不可用、角色不存在、等级/职业不合法、
// 任务记录与角色不一致时整请求失败，一个字节都不写。任务行写完后必须回读
// 校验每一条目标任务都是 completed，否则同样按失败返回。
func (o *Owner) ApplyCouponMainQuestClear(
	ctx context.Context,
	catalog *Catalog,
	input CouponClearInput,
) (CouponClearResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return CouponClearResult{}, ErrCouponClearUnavailable
	}
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	if input.CharacterID == "" || input.ClearedAt.IsZero() {
		return CouponClearResult{}, ErrCharacterRequired
	}
	if input.CutoffLevel <= 0 || input.CutoffLevel > input.Eligibility.Level {
		return CouponClearResult{}, ErrCouponClearLevelInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scope := input.Scope
	if scope == "" {
		scope = CouponClearScopeMainStorySweep
	}

	var plan AutoCompleteMainPlan
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx dnfrepo.Group) error {
		character, found, err := tx.Character.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		if character.Level != input.Eligibility.Level || input.CutoffLevel > character.Level {
			return ErrCouponClearLevelInvalid
		}
		record, found, err := tx.Quest.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			record = dnfrepo.QuestRecord{CharacterID: input.CharacterID}
		} else if strings.TrimSpace(record.CharacterID) != input.CharacterID {
			return ErrQuestPersistVerify
		}
		plan, err = catalog.PlanCouponMainQuestClear(record, input.Eligibility, input.CutoffLevel, scope, input.ClearedAt)
		if err != nil {
			return err
		}
		plan.Record.CharacterID = input.CharacterID
		if len(plan.ChangedFields) == 0 {
			return nil
		}
		return dnfrepo.SaveQuestFields(ctx, tx.Quest, plan.Record, plan.ChangedFields...)
	})
	if err != nil {
		return CouponClearResult{}, err
	}

	result := CouponClearResult{
		CharacterID:           input.CharacterID,
		CutoffLevel:           input.CutoffLevel,
		Scope:                 scope,
		EligibleQuestIDs:      append([]int64(nil), plan.EligibleQuestIDs...),
		ChangedQuestIDs:       append([]int64(nil), plan.ChangedQuestIDs...),
		ChangedLinkedSubIDs:   append([]int64(nil), plan.ChangedLinkedSubIDs...),
		HighestCompletedQuest: plan.HighestCompletedQuest,
		ChangedFields:         append([]dnfrepo.QuestField(nil), plan.ChangedFields...),
		IncludeSubQuests:      true,
		Idempotent:            len(plan.ChangedFields) == 0,
	}
	if result.Idempotent {
		// 没有任何改动时不必回读：调用方按幂等空操作处理。
		return result, nil
	}
	persisted, found, err := o.quests.Load(ctx, input.CharacterID)
	if err != nil {
		return CouponClearResult{}, err
	}
	if !found {
		return CouponClearResult{}, ErrQuestPersistVerify
	}
	for _, questID := range plan.ChangedQuestIDs {
		state, known := questStateFor(persisted, questID)
		if !known || !isCompletedQuestStatus(state.Status) {
			return CouponClearResult{}, fmt.Errorf("%w: quest=%d", ErrQuestPersistVerify, questID)
		}
	}
	// 闭包写进去的 `[sub]` 行同样必须回读校验，否则"父已完成、子仍可接取"
	// 会以"落库失败但回了成功"的形式残留。
	for _, questID := range plan.ChangedLinkedSubIDs {
		state, known := questStateFor(persisted, questID)
		if !known || !isCompletedQuestStatus(state.Status) {
			return CouponClearResult{}, fmt.Errorf("%w: linked sub quest=%d", ErrQuestPersistVerify, questID)
		}
	}
	result.PostCommitQuest = dnfrepo.CloneQuest(persisted)
	return result, nil
}

// couponMainQuestClearState 只写必要的任务行字段：Status / ProgressValue /
// Extra。它不复用 finish 路径，因此不会发放经验、金币或物品，也不会写任何
// 结算回执（与第七批 GM 觉醒/转职任务覆写同一条口径）。
func couponMainQuestClearState(
	state dnfrepo.QuestState,
	definition Definition,
	cutoffLevel int,
	clearedAt time.Time,
) dnfrepo.QuestState {
	state.Status = "completed"
	state.ProgressValue = 0
	state.RewardSelectIndex = 0
	state.UpdatedAt = clearedAt
	if state.Extra == nil {
		state.Extra = make(map[string]string, 8)
	}
	if state.Extra["pvf_path"] == "" {
		state.Extra["pvf_path"] = definition.Path
	}
	if state.Extra["quest_type"] == "" {
		state.Extra["quest_type"] = definition.Type
	}
	// reward_state=granted 是"这条任务永远不会再发奖励"的既有标记，
	// finish/hunt/clear-map 三条 owner 路径都会读它。
	state.Extra["reward_state"] = finishRewardGranted
	state.Extra["auto_completed"] = "true"
	// 这个标记对两条入口都成立：任务手册"任务完成"的批量动作（op1429，客户端
	// 免费按钮）与任务完成券（op515 action 167，消耗实物道具）。所以用中性的
	// "主线按等级清空"命名，而不是"券"。
	state.Extra["auto_complete_reason"] = "main_story_level_clear_no_reward"
	state.Extra["auto_complete_level_cutoff"] = strconv.Itoa(cutoffLevel)
	return state
}
