package quest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func storyAdd(c *Catalog, d Definition) { c.ordered = append(c.ordered, d); c.byID[d.ID] = d }
func storyOrdinary(id int64, pre ...int64) Definition {
	d := Definition{ID: id, Grade: "[epic]", Type: "[meet npc]", Job: "[all]", LevelMin: 1, LevelMax: 99, ExposedByNPC: 1, RewardType: "[item]", NoExperience: true}
	if len(pre) > 0 {
		d.PreRequiredGroups = [][]int64{pre}
	}
	return d
}
func storyChar() repo.CharacterRecord {
	return repo.CharacterRecord{CharacterID: "19", AccountID: "acc", Job: "2", Level: 90, Stats: map[string]int64{"grow_type": 0, "exp": 0, "gold": 432}}
}
func storyCompletedAnswer(answer int) repo.QuestState {
	return repo.QuestState{Status: "completed", Extra: map[string]string{questionAnswerExtraKey: fmt.Sprint(answer), questionOptionCountExtraKey: "3", questionAnswerMetadataKey: "1", "reward_state": "granted"}}
}

func TestStorySweepExplicitBranchesAndDamagedSave(t *testing.T) {
	for _, mode := range []string{"ticket", "handbook"} {
		for answer := -1; answer < 4; answer++ {
			t.Run(fmt.Sprintf("%s/%d", mode, answer), func(t *testing.T) {
				c := questionOwnerTestCatalog(t)
				r := repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{3867: {Status: "completed"}, 3868: storyCompletedAnswer(answer)}}
				if answer == -1 {
					r.States[3868] = repo.QuestState{Status: "completed", Extra: map[string]string{"reward_state": "granted"}}
				}
				before := repo.CloneQuest(r)
				var p AutoCompleteMainPlan
				var err error
				if mode == "ticket" {
					p, err = c.PlanLevelUpTicketQuestClear(r, CharacterEligibility{Level: 89, Job: 2}, time.Now())
				} else {
					p, err = c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 2}, 89, "", time.Now())
				}
				if err != nil {
					t.Fatal(err)
				}
				for a, id := range []int64{3870, 3871, 3873} {
					if isCompletedQuestStatus(p.Record.States[id].Status) != (answer == a) {
						t.Fatalf("branch %d answer %d: %+v", id, answer, p)
					}
				}
				for _, id := range []int64{3869, 3872, 3874} {
					if isCompletedQuestStatus(p.Record.States[id].Status) {
						t.Fatal("other choice completed", id)
					}
				}
				if !reflect.DeepEqual(p.Record.States[3868], before.States[3868]) || !reflect.DeepEqual(r, before) {
					t.Fatal("answer or input changed")
				}
				if (len(p.DamagedQuestionIDs) > 0) != (answer < 0 || answer > 2) {
					t.Fatal("damaged-state classification", p.DamagedQuestionIDs)
				}
			})
		}
	}
}

func TestStorySweepTwoAnswerJobAndChoiceSubProtection(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	d := c.byID[3869]
	d.Job = "[at swordman]"
	c.byID[d.ID] = d
	for i := range c.ordered {
		if c.ordered[i].ID == d.ID {
			c.ordered[i] = d
		}
	}
	sub := storyOrdinary(6000)
	sub.Grade = "[sub]"
	sub.MainQuestID = 3867
	sub.Type = "[question]"
	sub.QuestionOptionCount = 2
	storyAdd(c, sub)
	for _, job := range []int{0, 11} {
		p, err := c.PlanLevelUpTicketQuestClear(repo.QuestRecord{States: map[int64]repo.QuestState{3867: {Status: "completed"}, 6000: {Status: "active"}}}, CharacterEligibility{Level: 89, Job: job}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if containsStoryQuest(p.BlockedQuestionIDs, 3869) != (job == 11) {
			t.Fatal("job guard", job, p.BlockedQuestionIDs)
		}
		if p.Record.States[6000].Status != "active" {
			t.Fatal("sub question auto-completed")
		}
	}
	for answer := 0; answer < 2; answer++ {
		r := repo.QuestRecord{States: map[int64]repo.QuestState{3867: {Status: "completed"}, 3869: {Status: "completed", Extra: map[string]string{questionAnswerExtraKey: fmt.Sprint(answer), questionOptionCountExtraKey: "2"}}}}
		p, err := c.PlanLevelUpTicketQuestClear(r, CharacterEligibility{Level: 89, Job: 11}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for a, id := range []int64{3872, 3874} {
			if isCompletedQuestStatus(p.Record.States[id].Status) != (answer == a) {
				t.Fatal("two-option branch", answer, id)
			}
		}
	}
}

func TestTicketStoryAuthorizationPersistsMergesAndResumesNoReward(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	// Independent low gates and the legitimate two-stage town chain.
	for _, id := range []int64{3156, 3280, 3329, 3616, 3622} {
		storyAdd(c, storyOrdinary(id))
	}
	storyAdd(c, storyOrdinary(3944, 3873))
	storyAdd(c, storyOrdinary(4840, 3944))
	future := storyOrdinary(7000, 4840)
	future.LevelMin = 90
	storyAdd(c, future)
	for _, id := range []int64{2888, 2900, 12855} {
		d := storyOrdinary(id)
		d.RewardType = "[hell challenge]"
		storyAdd(c, d)
	}
	sel := storyOrdinary(7001)
	sel.RewardSelectionItems = []RewardItemRule{{ItemID: 42, Count: 1}}
	storyAdd(c, sel)
	character := storyChar()
	now := time.Unix(1800000000, 0)
	r := repo.QuestRecord{CharacterID: character.CharacterID, States: map[int64]repo.QuestState{}}
	p, err := c.AuthorizeLevelUpTicketStory(&character, r, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3156, 3280, 3329, 3616, 3622} {
		if p.Record.States[id].Status != "completed" {
			t.Fatal("independent gate", id)
		}
	}
	a, ok, err := ReadTicketStoryAuthorization(character)
	if err != nil || !ok || a.Cutoff != 89 || a.Generation != 1 || len(a.BlockedQuestionIDs) != 2 {
		t.Fatal(a, ok, err)
	}
	if !containsStoryQuest(a.BlockedQuestionIDs, 3868) {
		t.Fatal(a)
	}
	// Persistence round-trip, no in-process owner memory required.
	raw, _ := json.Marshal(character)
	var restored repo.CharacterRecord
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	r = p.Record
	r.States[3868] = storyCompletedAnswer(2)
	beforeStats := repo.CloneCharacter(restored).Stats
	p, changed, err := c.ResumeLevelUpTicketStory(&restored, r, 3868, now.Add(time.Hour))
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	for _, id := range []int64{3873, 3944, 4840} {
		s := p.Record.States[id]
		if s.Status != "completed" || s.Extra["reward_state"] != "granted" || s.Extra["settlement_receipt_v1"] != "" {
			t.Fatal("bad no-reward completion", id, s)
		}
	}
	for _, id := range []int64{3870, 3871, 3872, 3874, 2888, 2900, 12855, 7000, 7001} {
		if isCompletedQuestStatus(p.Record.States[id].Status) {
			t.Fatal("out of scope", id)
		}
	}
	for k, v := range beforeStats {
		if k == "gold" || k == "exp" || k == "grow_type" {
			if restored.Stats[k] != v {
				t.Fatal("asset changed", k)
			}
		}
	}
	again, changed, err := c.ResumeLevelUpTicketStory(&restored, p.Record, 3868, now.Add(2*time.Hour))
	if err != nil || changed || len(again.ChangedFields) > 0 {
		t.Fatal("repeat event writes", changed, err)
	}
	// A second real ticket raises only its own cutoff/generation; no other policy.
	restored.Level = 91
	next, err := c.AuthorizeLevelUpTicketStory(&restored, p.Record, 101, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ = ReadTicketStoryAuthorization(restored)
	if a.Cutoff != 90 || a.Generation != 2 || a.SourceItemID != 101 || next.Record.States[7000].Status != "completed" {
		t.Fatal(a)
	}
	restored.Stats[ticketAuthorizationPrefix+"version"] = 2
	if _, err = c.AuthorizeLevelUpTicketStory(&restored, next.Record, 101, now); !errors.Is(err, ErrTicketStoryAuthorization) {
		t.Fatal("unknown policy merged", err)
	}
}

func storyFinishFixture(t *testing.T) (repo.Group, *Catalog, FinishCommitInput) {
	t.Helper()
	ctx := context.Background()
	g := finishOwnerSeed(t, ctx)
	c := questionOwnerTestCatalog(t)
	for i := range c.ordered {
		c.ordered[i].NoExperience = true
		c.byID[c.ordered[i].ID] = c.ordered[i]
	}
	character, _, _ := g.Character.Load(ctx, "19")
	character.Level = 2
	character.Stats["exp"] = 100
	q := repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{}}
	at := time.Unix(1800000000, 0)
	p, err := c.AuthorizeLevelUpTicketStory(&character, q, 100, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	p.Record.States[3868] = repo.QuestState{Status: "active", UpdatedAt: at, ProgressValue: 1}
	if err = g.Quest.Save(ctx, p.Record); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := g.Skill.Load(ctx, "19")
	skill.Points.SyncedLevel = 2
	skill.Points.TotalSP = 30
	skill.Points.RemainingSP = 30
	if err = g.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	input := FinishCommitInput{AccountID: "acc", CharacterID: "19", QuestID: 3868, Multiplier: 1, CommittedAt: at.Add(time.Minute), Progression: finishOwnerProgression(t)}
	return g, c, input
}

func TestTicketStoryNormalQuestionFinishAtomicResumeAndConcurrentRetry(t *testing.T) {
	ctx := context.Background()
	g, c, input := storyFinishFixture(t)
	o, _ := NewOwner(g)
	if _, err := o.ApplyFinishSettlement(ctx, c, input); !errors.Is(err, ErrFinishQuestionAnswerRequired) {
		t.Fatal(err)
	}
	cmd := questionOwnerCommand(19, 3868, 0, 2, true)
	if _, err := o.ApplyQuestionAnswer(ctx, c, cmd); err != nil {
		t.Fatal(err)
	}
	answered, _, _ := g.Quest.Load(ctx, "19")
	if answered.States[3873].Status != "" {
		t.Fatal("answer before finish triggered resume")
	}
	beforeInventory, _, _ := g.Inventory.Load(ctx, "19")
	beforeSkill, _, _ := g.Skill.Load(ctx, "19")
	// Recreate the owner after answer, as after a disconnect/server restart.
	o, _ = NewOwner(g)
	var wg sync.WaitGroup
	results := make(chan FinishCommitResult, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := o.ApplyFinishSettlement(ctx, c, input); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	commits := 0
	for r := range results {
		if !r.Replayed {
			commits++
		}
		if !r.AtomicCommitted || r.PostCommitQuest.States[3873].Status != "completed" || !containsStoryQuest(r.ResumedStoryQuestIDs, 3873) {
			t.Fatal("missing atomic continuation", r)
		}
	}
	if commits != 1 {
		t.Fatal("duplicate finish", commits)
	}
	q, _, _ := g.Quest.Load(ctx, "19")
	character, _, _ := g.Character.Load(ctx, "19")
	a, _, err := ReadTicketStoryAuthorization(character)
	if err != nil || a.Generation != 1 || a.Revision != 2 {
		t.Fatal(a, err)
	}
	if q.States[3868].Extra[questionAnswerExtraKey] != "2" || q.States[3870].Status != "" || q.States[3871].Status != "" {
		t.Fatal("branch changed")
	}
	inv, _, _ := g.Inventory.Load(ctx, "19")
	skill, _, _ := g.Skill.Load(ctx, "19")
	if !reflect.DeepEqual(inv.Slots, beforeInventory.Slots) || !reflect.DeepEqual(skill.Points, beforeSkill.Points) || character.Level != 2 || character.Stats["exp"] != 100 {
		t.Fatalf("resume assets: inv=%+v before=%+v points=%+v before=%+v level=%d exp=%d", inv.Slots, beforeInventory.Slots, skill.Points, beforeSkill.Points, character.Level, character.Stats["exp"])
	}
}

type storyFailSettlement struct {
	inner repo.CharacterSettlementUnitOfWork
	err   error
}

func (f storyFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	return f.inner.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return f.err
	})
}
func TestTicketStoryAnswerAndFinishFailureRollBack(t *testing.T) {
	ctx := context.Background()
	g, c, input := storyFinishFixture(t)
	base := g
	failure := errors.New("commit failure")
	g.CharacterSettlement = storyFailSettlement{base.CharacterSettlement, failure}
	o, _ := NewOwner(g)
	qBefore, _, _ := base.Quest.Load(ctx, "19")
	charBefore, _, _ := base.Character.Load(ctx, "19")
	if _, err := o.ApplyQuestionAnswer(ctx, c, questionOwnerCommand(19, 3868, 0, 2, true)); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	q, _, _ := base.Quest.Load(ctx, "19")
	if !reflect.DeepEqual(qBefore, q) {
		t.Fatal("answer escaped rollback")
	}
	good, _ := NewOwner(base)
	if _, err := good.ApplyQuestionAnswer(ctx, c, questionOwnerCommand(19, 3868, 0, 2, true)); err != nil {
		t.Fatal(err)
	}
	qBefore, _, _ = base.Quest.Load(ctx, "19")
	if _, err := o.ApplyFinishSettlement(ctx, c, input); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	q, _, _ = base.Quest.Load(ctx, "19")
	ch, _, _ := base.Character.Load(ctx, "19")
	if !reflect.DeepEqual(qBefore, q) || !reflect.DeepEqual(charBefore, ch) {
		t.Fatal("finish/resume half committed")
	}
	if _, err := good.ApplyFinishSettlement(ctx, c, input); err != nil {
		t.Fatal("safe retry", err)
	}
}

func BenchmarkTicketStoryNoAuthorization(b *testing.B) {
	c := &Catalog{}
	ch := repo.CharacterRecord{CharacterID: "fixture"}
	q := repo.QuestRecord{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = c.ResumeLevelUpTicketStory(&ch, q, 3868, time.Time{})
	}
}

func TestTicketStoryMultipleQuestionEventsAndPolicyIsolation(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	d := storyOrdinary(6000, 3873)
	d.Type = "[question]"
	d.QuestionOptionCount = 3
	storyAdd(c, d)
	tail := storyOrdinary(6001)
	tail.PreRequiredAnswers = []int64{6000, 1}
	storyAdd(c, tail)
	ch := storyChar()
	at := time.Unix(1800000000, 0)
	r := repo.QuestRecord{CharacterID: ch.CharacterID, States: map[int64]repo.QuestState{}}
	p, e := c.AuthorizeLevelUpTicketStory(&ch, r, 100, at)
	if e != nil {
		t.Fatal(e)
	}
	r = p.Record
	r.States[3868] = storyCompletedAnswer(2)
	p, _, e = c.ResumeLevelUpTicketStory(&ch, r, 3868, at)
	if e != nil {
		t.Fatal(e)
	}
	if !containsStoryQuest(p.BlockedQuestionIDs, 6000) || p.Record.States[6001].Status != "" {
		t.Fatal("second decision bypassed")
	}
	r = p.Record
	r.States[6000] = storyCompletedAnswer(1)
	p, _, e = c.ResumeLevelUpTicketStory(&ch, r, 6000, at)
	if e != nil || p.Record.States[6001].Status != "completed" {
		t.Fatal("second resume", e)
	}
	a, _, _ := ReadTicketStoryAuthorization(ch)
	if a.Generation != 1 || a.Revision != 3 {
		t.Fatal("resume manufactured ticket", a)
	}
	// Manual/coupon clearing can never create or borrow ticket authorization.
	ungranted := storyChar()
	ungranted.Stats["some_other_policy_cutoff"] = 89
	before := repo.CloneCharacter(ungranted)
	_, changed, e := c.ResumeLevelUpTicketStory(&ungranted, r, 6000, at)
	if e != nil || changed || !reflect.DeepEqual(before, ungranted) {
		t.Fatal("another policy authorized resume")
	}
}

func TestStorySweepCorruptQuestionCannotLaunderCompletedDescendant(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	storyAdd(c, storyOrdinary(6001, 3873))
	r := repo.QuestRecord{States: map[int64]repo.QuestState{3868: {Status: "completed"}, 3873: {Status: "completed"}}}
	p, e := c.PlanCouponMainQuestClear(r, CharacterEligibility{Level: 90, Job: 0}, 89, "", time.Now())
	if e != nil || p.Record.States[6001].Status != "" || !containsStoryQuest(p.DamagedQuestionIDs, 3868) {
		t.Fatal("damaged chain laundered", e)
	}
}
func BenchmarkStorySweepCatalogChain(b *testing.B) {
	c := &Catalog{byID: map[int64]Definition{}}
	for i := int64(1); i <= 3100; i++ {
		d := storyOrdinary(i)
		if i > 1 {
			d.PreRequiredGroups = [][]int64{{i - 1}}
		}
		storyAdd(c, d)
	}
	ch := CharacterEligibility{Level: 89, Job: 2}
	q := repo.QuestRecord{CharacterID: "fixture"}
	at := time.Unix(1800000000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.PlanLevelUpTicketQuestClear(q, ch, at)
	}
}

func TestTicketStoryAuthorizationCannotCrossCharacters(t *testing.T) {
	c := questionOwnerTestCatalog(t)
	ch := storyChar()
	r := repo.QuestRecord{CharacterID: "another-character"}
	before := repo.CloneCharacter(ch)
	if _, err := c.AuthorizeLevelUpTicketStory(&ch, r, 100, time.Unix(1800000000, 0)); !errors.Is(err, ErrTicketStoryAuthorization) {
		t.Fatal("cross-character grant accepted", err)
	}
	if !reflect.DeepEqual(before, ch) {
		t.Fatal("failed grant changed character")
	}
	r.CharacterID = ch.CharacterID
	p, err := c.AuthorizeLevelUpTicketStory(&ch, r, 100, time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	r = p.Record
	r.States[3868] = storyCompletedAnswer(2)
	r.CharacterID = "another-character"
	before = repo.CloneCharacter(ch)
	if _, _, err = c.ResumeLevelUpTicketStory(&ch, r, 3868, time.Unix(1800000100, 0)); !errors.Is(err, ErrTicketStoryAuthorization) {
		t.Fatal("cross-character resume accepted", err)
	}
	if !reflect.DeepEqual(before, ch) {
		t.Fatal("failed resume changed character")
	}
}
