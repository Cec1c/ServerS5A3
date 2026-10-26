package quest

import (
	"fmt"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// PlanLevelUpTicketQuestClear 关闭目标等级之前可达的主线/普通任务及已激活子任务。
// 保留前置、碰撞、分支和职业规则；不授予普通任务奖励或改写职业/栏位/深渊权限。
func (c *Catalog) PlanLevelUpTicketQuestClear(record repo.QuestRecord, character CharacterEligibility, at time.Time) (AutoCompleteMainPlan, error) {
	if c == nil || !definitionCharacterJobValid(character) || at.IsZero() {
		return AutoCompleteMainPlan{}, ErrAutoCompleteMainUnavailable
	}
	plan := AutoCompleteMainPlan{Record: repo.CloneQuest(record)}
	if plan.Record.States == nil {
		plan.Record.States = map[int64]repo.QuestState{}
	}
	eligible := func(d Definition) bool {
		grade := normalizeQuestTag(d.Grade)
		return (grade == "epic" || grade == "normal" || grade == "") && d.LevelMin <= character.Level && d.ExposedByNPC != 0 && !d.IsEvent &&
			!d.HasCreatureRequirement && !d.HasExpertRequirement && d.JobChangeQuest == 0 && definitionMatchesCharacter(d, character) && levelUpTicketQuestRewardOrdinary(d)
	}
	for {
		completed, _ := questStateSets(plan.Record)
		progressed := false
		for _, d := range c.ordered {
			if !eligible(d) {
				continue
			}
			if _, done := completed[d.ID]; done {
				continue
			}
			state, field, known := mutableQuestState(&plan.Record, d.ID)
			wasActive := known && isActiveQuestStatus(state.Status)
			if known && !wasActive {
				continue
			}
			if !known {
				decision, err := c.DecideAcceptAt(character, plan.Record, d.ID, at)
				if err != nil || decision != AcceptNew {
					continue
				}
				state = repo.QuestState{}
				field = repo.QuestFieldStates
			}
			state = autoCompleteMainQuestState(state, d, character.Level, at)
			state.Extra["auto_complete_reason"] = "level_up_ticket_previous_levels_ordinary_epic_no_reward"
			if err := putLevelUpTicketQuestState(&plan, d.ID, state, field); err != nil {
				return AutoCompleteMainPlan{}, err
			}
			plan.EligibleQuestIDs = append(plan.EligibleQuestIDs, d.ID)
			if wasActive {
				plan.ActiveCompletedCount++
			}
			if d.ID > plan.HighestCompletedQuest {
				plan.HighestCompletedQuest = d.ID
			}
			for _, sub := range c.ordered {
				if sub.MainQuestID != d.ID || normalizeQuestTag(sub.Grade) != "sub" || sub.LevelMin > character.Level || sub.IsEvent || sub.JobChangeQuest != 0 || !definitionMatchesCharacter(sub, character) || !levelUpTicketQuestRewardOrdinary(sub) {
					continue
				}
				linked, linkedField, known := mutableQuestState(&plan.Record, sub.ID)
				if !known || !isActiveQuestStatus(linked.Status) {
					continue
				}
				linked = autoCompleteMainQuestState(linked, sub, character.Level, at)
				linked.Extra["auto_complete_reason"] = "level_up_ticket_linked_objective_no_reward"
				if err := putLevelUpTicketQuestState(&plan, sub.ID, linked, linkedField); err != nil {
					return AutoCompleteMainPlan{}, err
				}
				plan.ChangedLinkedSubIDs = append(plan.ChangedLinkedSubIDs, sub.ID)
			}
			progressed = true
			break
		}
		if !progressed {
			break
		}
	}
	if len(plan.ChangedFields) > 0 {
		plan.Record.UpdatedAt = at
	}
	return plan, nil
}

func levelUpTicketQuestRewardOrdinary(d Definition) bool {
	if d.RewardParseError != "" {
		return false
	}
	switch normalizeQuestTag(d.RewardType) {
	case "", "item", "gold":
		return true
	default:
		return false
	}
}
func putLevelUpTicketQuestState(plan *AutoCompleteMainPlan, id int64, state repo.QuestState, field repo.QuestField) error {
	switch field {
	case repo.QuestFieldStates:
		plan.Record.States[id] = state
	case repo.QuestFieldProgress:
		plan.Record.Progress[id] = state
	default:
		return fmt.Errorf("升级券任务字段不支持：%s", field)
	}
	plan.ChangedQuestIDs = append(plan.ChangedQuestIDs, id)
	plan.ChangedFields = mergeQuestChangedField(plan.ChangedFields, field)
	return nil
}
