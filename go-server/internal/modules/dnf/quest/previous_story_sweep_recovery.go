package quest

import (
	"context"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// PreviousMainStorySweepCutoff recognizes a persisted, previously requested
// full-story clear. Manual completions, active rows and single-entry clears do
// not authorize a new batch. Canonical states override legacy progress rows.
func PreviousMainStorySweepCutoff(record dnfrepo.QuestRecord, level int) int {
	cutoff := 0
	seen := make(map[int64]bool, len(record.States))
	visit := func(id int64, state dnfrepo.QuestState) {
		if seen[id] {
			return
		}
		seen[id] = true
		if !isCompletedQuestStatus(state.Status) || state.Extra["auto_completed"] != "true" ||
			state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" {
			return
		}
		value, err := strconv.Atoi(state.Extra["auto_complete_level_cutoff"])
		if err == nil && value > cutoff && value > 0 && value < level {
			cutoff = value
		}
	}
	for id, state := range record.States {
		visit(id, state)
	}
	for id, state := range record.Progress {
		visit(id, state)
	}
	return cutoff
}

// RecoverPreviousMainStorySweep repeats only the already-authorized historic
// range when an attained awakening is still absent. Eligibility, old evidence,
// quest writes and persistence verification share the normal owner transaction.
func (o *Owner) RecoverPreviousMainStorySweep(ctx context.Context, catalog *Catalog, input AutoCompleteMainInput) (AutoCompleteMainResult, error) {
	return o.applyAutoCompleteMain(ctx, catalog, input, true)
}

func (c *Catalog) planPreviousMainStorySweepRecovery(record dnfrepo.QuestRecord, character CharacterEligibility, limit int, at time.Time) (AutoCompleteMainPlan, int, error) {
	unchanged := AutoCompleteMainPlan{Record: dnfrepo.CloneQuest(record)}
	cutoff := PreviousMainStorySweepCutoff(record, character.Level)
	if cutoff > limit {
		cutoff = limit
	}
	if cutoff <= 0 {
		return unchanged, cutoff, nil
	}
	missing := false
	for _, definition := range c.ordered {
		if definition.LevelMin <= cutoff && storySweepAchievedAwakening(definition, character) && !IsCompleted(record, definition.ID) {
			missing = true
			break
		}
	}
	if !missing {
		return unchanged, cutoff, nil
	}
	plan, err := c.PlanAutoCompleteMainQuests(record, character, cutoff, 0, CouponClearScopeMainStorySweep, at)
	return plan, cutoff, err
}
