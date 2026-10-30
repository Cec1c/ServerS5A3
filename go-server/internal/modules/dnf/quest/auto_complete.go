package quest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrAutoCompleteMainUnavailable = errors.New("main quest auto-complete is unavailable")
	ErrAutoCompleteLevelInvalid    = errors.New("main quest auto-complete level is invalid")
	ErrAutoCompleteTargetInvalid   = errors.New("main quest auto-complete target is invalid")
	ErrAutoCompleteTargetAmbiguous = errors.New("main quest auto-complete target is ambiguous")
)

type AutoCompleteMainInput struct {
	AccountID     string
	CharacterID   string
	Eligibility   CharacterEligibility
	CutoffLevel   int
	TargetQuestID int64
	// Scope 只在 TargetQuestID == 0（批量动作）时有意义，取值见
	// CouponClearScope。留空等价于 CouponClearScopeMainStorySweep。
	Scope       CouponClearScope
	CompletedAt time.Time
	// Revalidate checks the reserved live character binding before mutation.
	Revalidate func() bool
}

type AutoCompleteMainPlan struct {
	BlockedQuestionIDs    []int64
	DamagedQuestionIDs    []int64
	Record                dnfrepo.QuestRecord
	EligibleQuestIDs      []int64
	ChangedQuestIDs       []int64
	ChangedLinkedSubIDs   []int64
	ActiveCompletedCount  int
	HighestCompletedQuest int64
	ChangedFields         []dnfrepo.QuestField
}

type AutoCompleteMainResult struct {
	BlockedQuestionIDs    []int64
	DamagedQuestionIDs    []int64
	PostCommitCharacter   dnfrepo.CharacterRecord
	CharacterID           string
	CutoffLevel           int
	EligibleQuestIDs      []int64
	ChangedQuestIDs       []int64
	ChangedLinkedSubIDs   []int64
	ActiveCompletedCount  int
	HighestCompletedQuest int64
	PostCommitQuest       dnfrepo.QuestRecord
	Idempotent            bool
}

// PlanAutoCompleteMainQuests 处理任务手册 op1429 的两种请求形状：
//
//   - targetQuestID != 0：只关闭这一条指定的、当前处于激活态的 PVF `[epic]`
//     任务（UI 上的一行）；
//   - targetQuestID == 0：当前 EXE 的"任务完成"确认框发的就是零 selector，
//     语义是批量动作。批量口径**只有一份实现**
//     （PlanCouponMainQuestClear），由 scope 决定范围：
//     CouponClearScopeMainStorySweep（默认，与确认框文案「自动完成 Lv89 为止的
//     主线任务」一致）或 CouponClearScopeActiveOnly（2026-09-25 之前的旧口径）。
//
// 两种形状都不发奖励，也不改角色、背包、技能或货币聚合。
func (c *Catalog) PlanAutoCompleteMainQuests(
	record dnfrepo.QuestRecord,
	character CharacterEligibility,
	cutoffLevel int,
	targetQuestID int64,
	scope CouponClearScope,
	completedAt time.Time,
) (AutoCompleteMainPlan, error) {
	if c == nil {
		return AutoCompleteMainPlan{}, ErrCatalogEmpty
	}
	if cutoffLevel <= 0 || cutoffLevel >= character.Level || !definitionCharacterJobValid(character) {
		return AutoCompleteMainPlan{}, ErrAutoCompleteLevelInvalid
	}
	if completedAt.IsZero() {
		return AutoCompleteMainPlan{}, ErrAutoCompleteMainUnavailable
	}
	if targetQuestID == 0 {
		// 批量动作（零 selector）：唯一实现在 PlanCouponMainQuestClear。
		return c.PlanCouponMainQuestClear(record, character, cutoffLevel, scope, completedAt)
	}

	plan := AutoCompleteMainPlan{Record: dnfrepo.CloneQuest(record)}
	eligibleEpic := func(definition Definition) bool {
		return mainStoryQuestAutoClearable(definition, character, cutoffLevel)
	}
	definition, definitionKnown := c.Find(targetQuestID)
	if !definitionKnown || !eligibleEpic(definition) {
		return AutoCompleteMainPlan{}, ErrAutoCompleteTargetInvalid
	}
	state, field, stateKnown := mutableQuestState(&plan.Record, targetQuestID)
	if !stateKnown {
		return AutoCompleteMainPlan{}, ErrAutoCompleteTargetInvalid
	}
	plan.EligibleQuestIDs = []int64{targetQuestID}
	plan.HighestCompletedQuest = targetQuestID
	if isCompletedQuestStatus(state.Status) {
		return plan, nil
	}
	if !isActiveQuestStatus(state.Status) || !(storySweepOrdinary(definition) || storySweepAchievedAwakening(definition, character)) {
		return AutoCompleteMainPlan{}, ErrAutoCompleteTargetInvalid
	}
	completed, active, damaged := c.storySweepEvidence(plan.Record)
	plan.DamagedQuestionIDs = damaged
	if _, blocked := c.storySweepProtectedParents(character, completed)[definition.ID]; blocked {
		return AutoCompleteMainPlan{}, ErrAutoCompleteTargetInvalid
	}
	if !c.storySweepDependencies(definition, plan.Record, completed, active) {
		return AutoCompleteMainPlan{}, ErrAutoCompleteTargetInvalid
	}
	plan.ActiveCompletedCount = 1
	state = autoCompleteMainQuestState(state, definition, cutoffLevel, completedAt)
	if field == dnfrepo.QuestFieldStates {
		plan.Record.States[targetQuestID] = state
	} else {
		plan.Record.Progress[targetQuestID] = state
	}
	plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
	plan.ChangedQuestIDs = []int64{targetQuestID}
	// 同一套 `[sub]` 闭包：单条"任务完成"（op1429 带 selector）也要把这条主线的
	// `[sub]` 子任务一并关掉，否则同样的死节点会在单条路径上复现。
	maxSubQuestID := c.applyMainQuestSweepSubClosure(&plan, map[int64]struct{}{targetQuestID: {}}, cutoffLevel, completedAt)
	if plan.HighestCompletedQuest == 0 && maxSubQuestID > 0 {
		plan.HighestCompletedQuest = maxSubQuestID
	}
	if len(plan.ChangedFields) != 0 {
		plan.Record.UpdatedAt = completedAt
	}
	return plan, nil
}

func (o *Owner) ApplyAutoCompleteMain(
	ctx context.Context,
	catalog *Catalog,
	input AutoCompleteMainInput,
) (AutoCompleteMainResult, error) {
	return o.applyAutoCompleteMain(ctx, catalog, input, false)
}

func (o *Owner) applyAutoCompleteMain(
	ctx context.Context,
	catalog *Catalog,
	input AutoCompleteMainInput,
	recoverPreviousSweep bool,
) (AutoCompleteMainResult, error) {
	if o == nil || o.repositories.CharacterSettlement == nil || catalog == nil {
		return AutoCompleteMainResult{}, ErrAutoCompleteMainUnavailable
	}
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	input.AccountID = strings.TrimSpace(input.AccountID)
	if input.AccountID == "" || input.CharacterID == "" || input.CompletedAt.IsZero() {
		return AutoCompleteMainResult{}, ErrCharacterRequired
	}
	if input.CutoffLevel <= 0 || input.CutoffLevel >= input.Eligibility.Level {
		return AutoCompleteMainResult{}, ErrAutoCompleteLevelInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var plan AutoCompleteMainPlan
	var committedCharacter dnfrepo.CharacterRecord
	var persisted dnfrepo.QuestRecord
	err := o.repositories.CharacterSettlement.WithinCharacterSettlement(ctx, input.CharacterID, func(tx dnfrepo.Group) error {
		if input.Revalidate != nil && !input.Revalidate() {
			return ErrAutoCompleteMainUnavailable
		}
		character, found, err := tx.Character.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != input.CharacterID || strings.TrimSpace(character.AccountID) != input.AccountID {
			return ErrCharacterNotFound
		}
		if character.Level != input.Eligibility.Level || input.CutoffLevel >= character.Level {
			return ErrAutoCompleteLevelInvalid
		}
		job, jobErr := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
		grow := character.Stats["grow_type"]
		if jobErr != nil || !jobmap.Valid(int(job)) || int(job) != input.Eligibility.Job || grow < 0 || grow > 255 || int(grow) != input.Eligibility.GrowType {
			return ErrAutoCompleteMainUnavailable
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
		if recoverPreviousSweep {
			plan, input.CutoffLevel, err = catalog.planPreviousMainStorySweepRecovery(record, input.Eligibility, input.CutoffLevel, input.CompletedAt)
		} else {
			plan, err = catalog.PlanAutoCompleteMainQuests(record, input.Eligibility, input.CutoffLevel, input.TargetQuestID, input.Scope, input.CompletedAt)
		}
		if err != nil {
			return err
		}
		plan.Record.CharacterID = input.CharacterID
		committedCharacter = dnfrepo.CloneCharacter(character)
		persisted = dnfrepo.CloneQuest(record)
		if len(plan.ChangedFields) == 0 {
			return nil
		}
		if input.Revalidate != nil && !input.Revalidate() {
			return ErrAutoCompleteMainUnavailable
		}
		if err := dnfrepo.SaveQuestFields(ctx, tx.Quest, plan.Record, plan.ChangedFields...); err != nil {
			return err
		}
		persisted, found, err = tx.Quest.Load(ctx, input.CharacterID)
		if err != nil {
			return err
		}
		if !found || persisted.CharacterID != input.CharacterID {
			return ErrQuestPersistVerify
		}
		for _, questID := range append(append([]int64(nil), plan.EligibleQuestIDs...), plan.ChangedLinkedSubIDs...) {
			state, known := questStateFor(persisted, questID)
			if !known || !isCompletedQuestStatus(state.Status) {
				return fmt.Errorf("%w: quest=%d", ErrQuestPersistVerify, questID)
			}
		}
		persisted = dnfrepo.CloneQuest(persisted)
		return nil
	})
	if err != nil {
		return AutoCompleteMainResult{}, err
	}

	// Return the verified transaction snapshot. An unrelated later load must
	// never turn an already committed completion into a failed operation.
	return AutoCompleteMainResult{
		BlockedQuestionIDs:    append([]int64(nil), plan.BlockedQuestionIDs...),
		DamagedQuestionIDs:    append([]int64(nil), plan.DamagedQuestionIDs...),
		PostCommitCharacter:   committedCharacter,
		CharacterID:           input.CharacterID,
		CutoffLevel:           input.CutoffLevel,
		EligibleQuestIDs:      append([]int64(nil), plan.EligibleQuestIDs...),
		ChangedQuestIDs:       append([]int64(nil), plan.ChangedQuestIDs...),
		ChangedLinkedSubIDs:   append([]int64(nil), plan.ChangedLinkedSubIDs...),
		ActiveCompletedCount:  plan.ActiveCompletedCount,
		HighestCompletedQuest: plan.HighestCompletedQuest,
		PostCommitQuest:       dnfrepo.CloneQuest(persisted),
		Idempotent:            len(plan.ChangedFields) == 0,
	}, nil
}

func definitionCharacterJobValid(character CharacterEligibility) bool {
	return character.Level > 0 && jobmap.Valid(character.Job)
}

// mainStoryQuestAutoClearable 是"这条主线任务能不能被一键完成"的唯一资格规则，
// 任务手册的 op1429 批量动作（PlanAutoCompleteMainQuests）与按等级完成券
// （PlanCouponMainQuestClear）共用它。
//
// 四个条件都来自运行时 PVF：`[grade] [epic]`（主线）、`[level]` 下限不高于
// cutoff、`[exposed by npc]`（能在任务手册里被玩家看到）、非活动任务；
// 最后一个条件是角色/职业/成长阶段谓词。副作用类、成就类、转职/觉醒类任务
// 都不在这条规则里。
func mainStoryQuestAutoClearable(definition Definition, character CharacterEligibility, cutoffLevel int) bool {
	return normalizeQuestTag(definition.Grade) == "epic" &&
		definition.LevelMin > 0 && definition.LevelMin <= cutoffLevel &&
		definition.ExposedByNPC != 0 && !definition.IsEvent &&
		definitionMatchesCharacter(definition, character)
}

func sortedQuestIDSet(values map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(values))
	for questID := range values {
		ids = append(ids, questID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func autoCompleteMainQuestState(state dnfrepo.QuestState, definition Definition, cutoffLevel int, completedAt time.Time) dnfrepo.QuestState {
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
	state.Extra["auto_complete_reason"] = "epic_below_character_level_no_reward"
	state.Extra["auto_complete_level_cutoff"] = strconv.Itoa(cutoffLevel)
	return state
}

func shouldAutoCompleteNoRewardSubQuest(definition Definition) bool {
	rewardType := normalizeQuestTag(definition.RewardType)
	legacyEmptyReward := rewardType == ""
	zeroItemPlaceholder := definition.NoExperience && rewardType == "item" &&
		len(definition.RewardIntData) == 2 &&
		definition.RewardIntData[0] == 0 && definition.RewardIntData[1] == 0
	return normalizeQuestTag(definition.Grade) == "sub" &&
		(legacyEmptyReward || zeroItemPlaceholder) && definition.RewardParseError == "" &&
		!definition.HasGoldReward && emptyRewardDataValid(definition.RewardIntData) &&
		len(definition.RewardItems) == 0 && len(definition.RewardSelectionItems) == 0
}

func autoCompleteNoRewardSubQuestState(state *dnfrepo.QuestState, definition Definition, completedAt time.Time) bool {
	if state == nil || !shouldAutoCompleteNoRewardSubQuest(definition) {
		return false
	}
	state.Status = "completed"
	state.ProgressValue = 0
	state.UpdatedAt = completedAt
	if state.Extra == nil {
		state.Extra = make(map[string]string, 8)
	}
	state.Extra["reward_state"] = finishRewardGranted
	state.Extra["auto_completed"] = "true"
	state.Extra["auto_complete_reason"] = "no_reward_sub_quest"
	return true
}

func mergeQuestChangedField(current []dnfrepo.QuestField, field dnfrepo.QuestField) []dnfrepo.QuestField {
	if field == "" {
		return current
	}
	for _, existing := range current {
		if existing == field {
			return current
		}
	}
	return append(current, field)
}

type LinkedSubQuestActivation struct {
	ParentID       int64
	QuestID        int64
	Field          dnfrepo.QuestField
	InitTrigger    uint32
	PVFPath        string
	QuestType      string
	AlreadyPresent bool
}

type LinkedSubQuestActivationPlan struct {
	Record        dnfrepo.QuestRecord
	Activations   []LinkedSubQuestActivation
	ChangedFields []dnfrepo.QuestField
}

func (c *Catalog) PlanActiveQuestClearLinkedSubQuestActivation(record dnfrepo.QuestRecord, activatedAt time.Time) (LinkedSubQuestActivationPlan, error) {
	if c == nil {
		return LinkedSubQuestActivationPlan{}, ErrCatalogEmpty
	}
	if activatedAt.IsZero() {
		activatedAt = time.Now()
	}
	plan := LinkedSubQuestActivationPlan{Record: dnfrepo.CloneQuest(record)}
	plan.Activations, plan.ChangedFields = c.activateMissingLinkedNoRewardSubQuests(&plan.Record, activatedAt, "")
	if len(plan.ChangedFields) > 0 {
		plan.Record.UpdatedAt = activatedAt
	}
	return plan, nil
}

func (c *Catalog) activateMissingLinkedNoRewardSubQuests(
	record *dnfrepo.QuestRecord,
	activatedAt time.Time,
	childType string,
) ([]LinkedSubQuestActivation, []dnfrepo.QuestField) {
	if c == nil {
		return nil, nil
	}
	childType = normalizeQuestTag(childType)
	completed, _ := questStateSets(*record)
	activations := make([]LinkedSubQuestActivation, 0)
	changedFields := make([]dnfrepo.QuestField, 0, 2)
	seenChildren := make(map[int64]struct{})
	collect := func(field dnfrepo.QuestField, states map[int64]dnfrepo.QuestState) {
		if len(states) == 0 {
			return
		}
		parentIDs := make([]int64, 0, len(states))
		for questID := range states {
			parentIDs = append(parentIDs, questID)
		}
		sort.Slice(parentIDs, func(left, right int) bool { return parentIDs[left] < parentIDs[right] })
		for _, parentID := range parentIDs {
			parentState := states[parentID]
			if !isActiveQuestStatus(parentState.Status) {
				continue
			}
			parent, known := c.Find(parentID)
			if !known {
				continue
			}
			if !questClearDefinition(parent) {
				continue
			}
			for _, childID := range parent.IntData {
				if childID <= 0 {
					continue
				}
				if _, duplicate := seenChildren[childID]; duplicate {
					continue
				}
				if _, known := questStateFor(*record, childID); known {
					continue
				}
				child, known := c.Find(childID)
				if !known || !linkedQuestChainChild(parent, child) ||
					(childType != "" && normalizeQuestTag(child.Type) != childType) {
					continue
				}
				seenChildren[childID] = struct{}{}
				initialTrigger := definitionInitialTrigger(child, completed)
				state := dnfrepo.QuestState{
					Status:        "active",
					ProgressValue: int64(initialTrigger),
					UpdatedAt:     activatedAt,
					Extra: map[string]string{
						"pvf_path":                              child.Path,
						"quest_type":                            child.Type,
						"main_quest_id":                         int64Text(parent.ID),
						"auto_activated_by_main_quest":          "true",
						"auto_activation_reason":                "active_quest_clear_parent_reconcile",
						"auto_activation_parent_progress_value": int64Text(parentState.ProgressValue),
					},
				}
				switch field {
				case dnfrepo.QuestFieldStates:
					if record.States == nil {
						record.States = make(map[int64]dnfrepo.QuestState, 1)
					}
					record.States[child.ID] = state
					changedFields = mergeQuestChangedField(changedFields, dnfrepo.QuestFieldStates)
				case dnfrepo.QuestFieldProgress:
					if record.Progress == nil {
						record.Progress = make(map[int64]dnfrepo.QuestState, 1)
					}
					record.Progress[child.ID] = state
					changedFields = mergeQuestChangedField(changedFields, dnfrepo.QuestFieldProgress)
				}
				activations = append(activations, LinkedSubQuestActivation{
					ParentID:    parent.ID,
					QuestID:     child.ID,
					Field:       field,
					InitTrigger: initialTrigger,
					PVFPath:     child.Path,
					QuestType:   child.Type,
				})
			}
		}
	}
	collect(dnfrepo.QuestFieldStates, record.States)
	collect(dnfrepo.QuestFieldProgress, record.Progress)
	return activations, changedFields
}

func int64Text(value int64) string {
	return strconv.FormatInt(value, 10)
}
