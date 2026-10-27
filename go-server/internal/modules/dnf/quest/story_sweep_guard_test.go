package quest

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestStorySweepUnansweredChoiceIsNotCompletion(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	for _, mode := range []string{"ticket", "handbook", "single"} {
		t.Run(mode, func(t *testing.T) {
			r := repo.QuestRecord{CharacterID: "fixture", States: map[int64]repo.QuestState{3867: {Status: "completed"}, 3868: {Status: "active"}}}
			var p AutoCompleteMainPlan
			var err error
			switch mode {
			case "ticket":
				p, err = c.PlanLevelUpTicketQuestClear(r, CharacterEligibility{Level: 89, Job: 0}, time.Now())
			case "handbook":
				p, err = c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 0}, 89, CouponClearScopeMainStorySweep, time.Now())
			case "single":
				p, err = c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0}, 89, 3868, "", time.Now())
				if err != nil {
					return
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{3868, 3869, 3870, 3871, 3872, 3873, 3874} {
				if isCompletedQuestStatus(p.Record.States[id].Status) {
					t.Fatalf("unanswered choice bypassed: %d", id)
				}
			}
		})
	}
}

func TestStorySweepHandbookMissingPrerequisite(t *testing.T) {
	d := Definition{ID: 500, Grade: "[epic]", Type: "[meet npc]", LevelMin: 1, LevelMax: 99, Job: "[all]", ExposedByNPC: 1, PreRequiredGroups: [][]int64{{499}}}
	c := &Catalog{ordered: []Definition{d}, byID: map[int64]Definition{500: d}}
	for _, active := range []bool{false, true} {
		r := repo.QuestRecord{CharacterID: "fixture", States: map[int64]repo.QuestState{}}
		if active {
			r.States[500] = repo.QuestState{Status: "active"}
		}
		p, err := c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 0}, 89, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if isCompletedQuestStatus(p.Record.States[500].Status) {
			t.Fatal("missing prerequisite bypassed", active)
		}
	}
}

func TestStorySweepParentCannotHideMandatoryQuestionChild(t *testing.T) {
	parent := storyOrdinary(100)
	parent.Type = "[quest clear]"
	child := storyOrdinary(101)
	child.Grade = "[sub]"
	child.MainQuestID = 100
	child.Type = "[question]"
	child.QuestionOptionCount = 3
	tail := storyOrdinary(102, 100)
	c := &Catalog{ordered: []Definition{parent, child, tail}, byID: map[int64]Definition{100: parent, 101: child, 102: tail}}
	r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "active"}, 101: {Status: "active"}}}
	p, e := c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 0}, 89, "", time.Now())
	if e != nil || len(p.ChangedFields) != 0 {
		t.Fatal("parent bypassed mandatory question", e, p)
	}
}

func TestStorySweepCanonicalActiveAnswerCannotBorrowProgressCompletion(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	answer := storyCompletedAnswer(2)
	answer.Status = "active"
	r := repo.QuestRecord{States: map[int64]repo.QuestState{3867: {Status: "completed"}, 3868: answer}, Progress: map[int64]repo.QuestState{3868: storyCompletedAnswer(2)}}
	for _, ticket := range []bool{false, true} {
		p := c.planStorySweep(r, CharacterEligibility{Level: 89, Job: 0}, 89, CouponClearScopeMainStorySweep, ticket, time.Now())
		if p.Record.States[3868].Status != "active" || isCompletedQuestStatus(p.Record.States[3873].Status) {
			t.Fatal("active answer bypassed normal finish", ticket)
		}
	}
}

func TestStorySweepDamagedCompletedBranchStillCollides(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	d := storyOrdinary(6000)
	d.CollisionQuests = []int64{3873}
	storyAdd(c, d)
	r := repo.QuestRecord{States: map[int64]repo.QuestState{3868: {Status: "completed"}, 3873: {Status: "completed"}}}
	p, e := c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 0}, 89, "", time.Now())
	if e != nil || p.Record.States[6000].Status != "" {
		t.Fatal("damaged branch allowed collision", e)
	}
}
