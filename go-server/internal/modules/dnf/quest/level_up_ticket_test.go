package quest

import (
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestLevelUpTicketQuestClearPreservesBranchesAndSpecialRewards(t *testing.T) {
	base := Definition{Grade: "[epic]", LevelMin: 1, LevelMax: 90, ExposedByNPC: 1, Job: "[all]", RewardType: "[item]"}
	c := &Catalog{byID: map[int64]Definition{}}
	add := func(id int64, change func(*Definition)) {
		d := base
		d.ID = id
		if change != nil {
			change(&d)
		}
		c.ordered = append(c.ordered, d)
		c.byID[id] = d
	}
	add(2, func(d *Definition) { d.PreRequiredGroups = [][]int64{{1}} })
	add(1, nil)
	add(3, func(d *Definition) { d.CollisionQuests = []int64{1} })
	add(4, func(d *Definition) { d.PreRequiredAnswers = []int64{1} })
	add(5, func(d *Definition) { d.LevelMin = 51 })
	add(6, func(d *Definition) { d.IsEvent = true })
	add(7, func(d *Definition) { d.Job = "[fighter]" })
	add(8, func(d *Definition) { d.JobChangeQuest = 1; d.RewardType = "[job change]" })
	add(9, func(d *Definition) { d.RewardType = "[slot extend]" })
	add(10, func(d *Definition) { d.RewardType = "[hell party]" })
	add(11, func(d *Definition) { d.Grade = "[daily]" })
	add(12, func(d *Definition) { d.Grade = "[repeat]" })
	add(13, func(d *Definition) { d.HasCreatureRequirement = true })
	add(14, func(d *Definition) { d.HasExpertRequirement = true })
	add(15, func(d *Definition) { d.Grade = "[sub]"; d.MainQuestID = 1 })
	add(16, func(d *Definition) { d.Grade = "[sub]"; d.MainQuestID = 1 })
	add(17, func(d *Definition) { d.RewardParseError = "bad reward" })
	add(18, func(d *Definition) { d.HasDependGiveItem = true })
	add(19, func(d *Definition) { d.Grade = "[normal]" })
	add(20, func(d *Definition) { d.ExposedByNPC = 0 })
	states := map[int64]repo.QuestState{1: {Status: "active"}, 5: {Status: "active"}, 8: {Status: "active"}, 9: {Status: "active"}, 10: {Status: "active"}, 11: {Status: "active"}, 17: {Status: "active"}}
	r := repo.QuestRecord{CharacterID: "2", States: states, Progress: map[int64]repo.QuestState{15: {Status: "active", ProgressValue: 8, Extra: map[string]string{"marker": "keep"}}}}
	before := repo.CloneQuest(r)
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	p, err := c.PlanLevelUpTicketQuestClear(r, CharacterEligibility{Level: 50, Job: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, before) {
		t.Fatal("计划阶段修改了原任务记录")
	}
	for _, id := range []int64{1, 2, 19} {
		s := p.Record.States[id]
		if s.Status != "completed" || s.Extra["reward_state"] != finishRewardGranted || s.Extra["auto_complete_level_cutoff"] != "50" {
			t.Fatalf("应关闭且不再领奖：%d=%+v", id, s)
		}
	}
	for _, id := range []int64{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 18, 20} {
		if !reflect.DeepEqual(p.Record.States[id], before.States[id]) {
			t.Fatalf("不应修改%d", id)
		}
	}
	if p.Record.Progress[15].Status != "completed" || p.Record.Progress[15].Extra["marker"] != "keep" || len(p.ChangedLinkedSubIDs) != 1 {
		t.Fatal("已激活子任务闭包不正确", p)
	}
	again, err := c.PlanLevelUpTicketQuestClear(p.Record, CharacterEligibility{Level: 50, Job: 2}, now)
	if err != nil || len(again.ChangedFields) != 0 || !reflect.DeepEqual(again.Record, p.Record) {
		t.Fatal("重复清理不幂等", err)
	}
}
