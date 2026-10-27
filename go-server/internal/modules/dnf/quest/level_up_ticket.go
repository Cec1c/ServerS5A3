package quest

import (
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// PlanLevelUpTicketQuestClear 关闭目标等级之前可达的主线/普通任务及已激活子任务。
// 保留前置、碰撞、分支和职业规则；不授予普通任务奖励或改写职业/栏位/深渊权限。
func (c *Catalog) PlanLevelUpTicketQuestClear(record repo.QuestRecord, character CharacterEligibility, at time.Time) (AutoCompleteMainPlan, error) {
	if c == nil || !definitionCharacterJobValid(character) || at.IsZero() {
		return AutoCompleteMainPlan{}, ErrAutoCompleteMainUnavailable
	}
	return c.planStorySweep(record, character, character.Level, CouponClearScopeMainStorySweep, true, at), nil
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
