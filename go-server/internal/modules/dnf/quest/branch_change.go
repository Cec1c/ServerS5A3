package quest

import (
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// PlanBranchChangeQuests 重建职业任务账，不发放职业任务或被跳过主线的奖励。
// 普通任务和专家职业保持；高级书的清主线范围由已核对物品政策单独传入。
func (c *Catalog) PlanBranchChangeQuests(record repo.QuestRecord, job, grow, level int, clearMain bool, at time.Time) (repo.QuestRecord, error) {
	reset, err := c.PlanBaseProfessionQuestReset(record, at)
	if err != nil {
		return repo.QuestRecord{}, err
	}
	q := reset.Record
	if q.States == nil {
		q.States = make(map[int64]repo.QuestState)
	}
	for _, id := range c.ProfessionProgressionQuestIDs(job, grow) {
		q.States[id] = repo.QuestState{Status: "completed", UpdatedAt: at, Extra: map[string]string{"completion_source": "profession_branch_change_no_reward", "reward_state": "granted"}}
	}
	if clearMain {
		plan, err := c.PlanCouponMainQuestClear(q, CharacterEligibility{Level: level, Job: job, GrowType: grow}, level-1, CouponClearScopeMainStorySweep, at)
		if err != nil {
			return repo.QuestRecord{}, err
		}
		q = plan.Record
	}
	q.UpdatedAt = at
	return q, nil
}

func (c *Catalog) HasActiveAwakeningQuest(record repo.QuestRecord) bool {
	for _, rows := range []map[int64]repo.QuestState{record.States, record.Progress} {
		for id, state := range rows {
			d, found := c.Find(id)
			if found && d.JobChangeQuest >= 2 && d.JobChangeQuest <= 3 && isActiveQuestStatus(state.Status) {
				return true
			}
		}
	}
	return false
}
