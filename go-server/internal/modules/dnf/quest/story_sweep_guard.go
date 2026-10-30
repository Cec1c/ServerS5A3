package quest

import (
	"strconv"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Automatic completion never substitutes for a player's answer or item choice.
// A Question remains active even after answering, until normal finish commits.
func storySweepOrdinary(d Definition) bool {
	return normalizeQuestTag(d.Type) != "question" && d.JobChangeQuest == 0 &&
		!d.IsEvent && !d.HasCreatureRequirement && !d.HasExpertRequirement &&
		len(d.RewardSelectionItems) == 0 && levelUpTicketQuestRewardOrdinary(d)
}

func validStoryQuestionAnswer(d Definition, s repo.QuestState) bool {
	a, ok := questionAnswerFromState(s)
	if !ok || d.QuestionOptionCount <= 0 || int(a) >= d.QuestionOptionCount {
		return false
	}
	// Legacy normal finishes stored the explicit answer before v1 was introduced.
	// Missing optional metadata is compatible; contradictory metadata is not.
	if v, ok := s.Extra[questionAnswerMetadataKey]; ok && v != questionAnswerMetadataVersion {
		return false
	}
	if v, ok := s.Extra[questionOptionCountExtraKey]; ok && v != strconv.Itoa(d.QuestionOptionCount) {
		return false
	}
	return true
}

// Remove damaged Question chains only from the planner's evidence set, never
// from the save. Already-completed rows are not permission to invent an answer.
func (c *Catalog) storySweepEvidence(r repo.QuestRecord) (map[int64]struct{}, map[int64]struct{}, []int64) {
	// Match questStateFor/mutableQuestState precedence: a stale Progress row
	// must not turn a still-active canonical States row into a finished choice.
	completed, active := map[int64]struct{}{}, map[int64]struct{}{}
	collect := func(id int64, s repo.QuestState) {
		if id <= 0 {
			return
		}
		if isCompletedQuestStatus(s.Status) {
			completed[id] = struct{}{}
		} else if isActiveQuestStatus(s.Status) {
			active[id] = struct{}{}
		}
	}
	for id, s := range r.States {
		collect(id, s)
	}
	for id, s := range r.Progress {
		if _, canonical := r.States[id]; !canonical {
			collect(id, s)
		}
	}
	damaged := map[int64]struct{}{}
	poisoned := map[int64]struct{}{}
	for _, d := range c.ordered {
		if normalizeQuestTag(d.Type) != "question" {
			continue
		}
		if _, done := completed[d.ID]; !done {
			continue
		}
		s, _ := questStateFor(r, d.ID)
		if !validStoryQuestionAnswer(d, s) {
			damaged[d.ID] = struct{}{}
			poisoned[d.ID] = struct{}{}
		}
	}
	if len(poisoned) > 0 {
		for pass := 0; pass < len(c.ordered); pass++ {
			changed := false
			for _, d := range c.ordered {
				if _, bad := poisoned[d.ID]; bad {
					continue
				}
				bad := false
				if len(d.PreRequiredAnswers) > 0 {
					_, bad = poisoned[d.PreRequiredAnswers[0]]
				}
				if !bad && len(d.PreRequiredGroups) > 0 {
					bad = true
					for _, group := range d.PreRequiredGroups {
						groupBad := false
						for _, id := range group {
							if _, found := poisoned[id]; found {
								groupBad = true
								break
							}
						}
						if !groupBad {
							bad = false
							break
						}
					}
				}
				if bad {
					poisoned[d.ID] = struct{}{}
					changed = true
				}
			}
			if !changed {
				break
			}
		}
		for id := range poisoned {
			delete(completed, id)
		}
	}
	return completed, active, sortedQuestIDSet(damaged)
}

// Shared dependency predicate, including active rows. Do not use AcceptIdempotent
// as proof that the prerequisites of an old active row are still satisfied.
func (c *Catalog) storySweepDependencies(d Definition, r repo.QuestRecord, completed, active map[int64]struct{}) bool {
	if len(d.PreRequiredGroups) > 0 {
		matched := false
		for _, group := range d.PreRequiredGroups {
			if completedAll(group, completed) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(d.PreRequiredAnswers) > 0 {
		if !questionAnswerPrerequisiteSatisfied(c, r, d.PreRequiredAnswers) {
			return false
		}
		if _, ok := completed[d.PreRequiredAnswers[0]]; !ok {
			return false
		}
	}
	for _, id := range d.CollisionQuests {
		// Damaged completed branches are unusable as prerequisites but remain
		// collisions. Never complete their mutually exclusive alternative.
		if s, ok := questStateFor(r, id); ok && (isCompletedQuestStatus(s.Status) || id != d.ID && isActiveQuestStatus(s.Status)) {
			return false
		}
		if _, ok := completed[id]; ok {
			return false
		}
		if id != d.ID {
			if _, ok := active[id]; ok {
				return false
			}
		}
	}
	return true
}

// A parent cannot hide an unfinished mandatory choice/special child by being
// auto-completed first. Follow only explicit main-quest ownership, not all later
// prerequisites. Existing completed parents are never rewritten.
func (c *Catalog) storySweepProtectedParents(ch CharacterEligibility, completed map[int64]struct{}) map[int64]struct{} {
	blocked := map[int64]struct{}{}
	for _, d := range c.ordered {
		if d.MainQuestID <= 0 || normalizeQuestTag(d.Grade) != "sub" || !definitionMatchesCharacter(d, ch) {
			continue
		}
		if _, done := completed[d.ID]; !done && !storySweepOrdinary(d) {
			blocked[d.MainQuestID] = struct{}{}
		}
	}
	for pass := 0; pass < len(c.ordered) && len(blocked) > 0; pass++ {
		changed := false
		for _, d := range c.ordered {
			if d.MainQuestID <= 0 {
				continue
			}
			if _, bad := blocked[d.ID]; bad {
				if _, exists := blocked[d.MainQuestID]; !exists {
					blocked[d.MainQuestID] = struct{}{}
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return blocked
}

// One bounded fixed-point sweep over an already-loaded catalog. Each pass must
// complete at least one new row. No per-row catalog reload or database access.
func (c *Catalog) planStorySweep(r repo.QuestRecord, ch CharacterEligibility, cutoff int, scope CouponClearScope, ticket bool, at time.Time) AutoCompleteMainPlan {
	p := AutoCompleteMainPlan{Record: repo.CloneQuest(r)}
	if p.Record.States == nil {
		p.Record.States = map[int64]repo.QuestState{}
	}
	completed, active, damaged := c.storySweepEvidence(p.Record)
	protectedParents := c.storySweepProtectedParents(ch, completed)
	p.DamagedQuestionIDs = damaged
	skippable := func(d Definition) bool {
		return storySweepOrdinary(d) || !ticket && storySweepAchievedAwakening(d, ch)
	}
	eligible := func(d Definition) bool {
		if ticket {
			grade := normalizeQuestTag(d.Grade)
			return (grade == "epic" || grade == "normal" || grade == "") && d.LevelMin <= cutoff &&
				d.ExposedByNPC != 0 && !d.IsEvent && !d.HasCreatureRequirement && !d.HasExpertRequirement &&
				d.JobChangeQuest == 0 && definitionMatchesCharacter(d, ch) && levelUpTicketQuestRewardOrdinary(d)
		}
		return mainStoryQuestAutoClearable(d, ch, cutoff) && (d.JobChangeQuest == 0 || storySweepAchievedAwakening(d, ch))
	}
	roots := map[int64]struct{}{}
	if scope != CouponClearScopeActiveOnly {
		for _, d := range c.ordered {
			if eligible(d) {
				if _, ok := completed[d.ID]; ok {
					roots[d.ID] = struct{}{}
				}
			}
		}
	}
	for pass := 0; pass <= len(c.ordered); pass++ {
		changed := false
		for _, d := range c.ordered {
			if !eligible(d) || !skippable(d) {
				continue
			}
			if _, blocked := protectedParents[d.ID]; blocked {
				continue
			}
			s, field, known := mutableQuestState(&p.Record, d.ID)
			if known && !isActiveQuestStatus(s.Status) {
				continue
			}
			if !known && scope == CouponClearScopeActiveOnly {
				continue
			}
			if !c.storySweepDependencies(d, p.Record, completed, active) {
				continue
			}
			if ticket && !known {
				if d.HasDependGiveItem || !c.definitionAcceptable(d, ch, p.Record, completed, active, false) || !c.activeFamilyAcceptable(d, ch, active) {
					continue
				}
			}
			if !known {
				field = repo.QuestFieldStates
			}
			s = couponMainQuestClearState(s, d, cutoff, at)
			if ticket {
				s.Extra["auto_complete_reason"] = "level_up_ticket_previous_levels_ordinary_epic_no_reward"
			}
			// mutableQuestState returns only States/Progress; missing rows use States.
			if field == repo.QuestFieldStates {
				p.Record.States[d.ID] = s
			} else {
				p.Record.Progress[d.ID] = s
			}
			p.ChangedQuestIDs = append(p.ChangedQuestIDs, d.ID)
			p.ChangedFields = mergeQuestChangedField(p.ChangedFields, field)
			p.EligibleQuestIDs = append(p.EligibleQuestIDs, d.ID)
			if known {
				p.ActiveCompletedCount++
			}
			if d.ID > p.HighestCompletedQuest {
				p.HighestCompletedQuest = d.ID
			}
			completed[d.ID] = struct{}{}
			delete(active, d.ID)
			roots[d.ID] = struct{}{}
			changed = true
		}
		// Ticket closes only active children. Handbook retains its explicit sub
		// closure, but every child now has the same choice/dependency safeguards.
		for _, d := range c.ordered {
			parent := d.MainQuestID
			if parent <= 0 || normalizeQuestTag(d.Grade) != "sub" {
				continue
			}
			if _, ok := roots[parent]; !ok {
				continue
			}
			if d.LevelMin > cutoff || !definitionMatchesCharacter(d, ch) || !storySweepOrdinary(d) {
				continue
			}
			if _, blocked := protectedParents[d.ID]; blocked {
				continue
			}
			s, field, known := mutableQuestState(&p.Record, d.ID)
			if known && !isActiveQuestStatus(s.Status) {
				continue
			}
			if ticket && !known {
				continue
			}
			if !c.storySweepDependencies(d, p.Record, completed, active) {
				continue
			}
			if !known {
				field = repo.QuestFieldStates
			}
			s = mainQuestSweepSubQuestState(s, d, parent, cutoff, at)
			if ticket {
				s.Extra["auto_complete_reason"] = "level_up_ticket_linked_objective_no_reward"
			}
			// Linked children are separate from main completion IDs for existing callers.
			if field == repo.QuestFieldStates {
				p.Record.States[d.ID] = s
			} else {
				p.Record.Progress[d.ID] = s
			}
			p.ChangedFields = mergeQuestChangedField(p.ChangedFields, field)
			p.ChangedLinkedSubIDs = append(p.ChangedLinkedSubIDs, d.ID)
			completed[d.ID] = struct{}{}
			delete(active, d.ID)
			roots[d.ID] = struct{}{}
			changed = true
			if p.HighestCompletedQuest == 0 {
				p.HighestCompletedQuest = d.ID
			}
		}
		if !changed {
			break
		}
	}
	for _, d := range c.ordered {
		if !eligible(d) || normalizeQuestTag(d.Type) != "question" {
			continue
		}
		if s, ok := questStateFor(p.Record, d.ID); ok && isCompletedQuestStatus(s.Status) {
			continue
		}
		if c.storySweepDependencies(d, p.Record, completed, active) {
			p.BlockedQuestionIDs = append(p.BlockedQuestionIDs, d.ID)
		}
	}
	if len(p.ChangedFields) > 0 {
		p.Record.UpdatedAt = at
	}
	return p
}

func containsStoryQuest(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
