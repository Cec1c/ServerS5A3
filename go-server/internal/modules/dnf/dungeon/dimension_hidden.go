package dungeon

import (
	"math"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// DimensionHiddenAdmission is prepared from server-owned PVF maze selection.
// It never accepts a client-provided probability, counter or reward choice.
type DimensionHiddenAdmission struct {
	ExpectedSequence  int64
	Encountered       bool
	WeeklyRewardLimit int64
}

func applyDimensionHiddenAdmission(character *dnfrepo.CharacterRecord, now time.Time, plan *DimensionHiddenAdmission) (bool, error) {
	if plan == nil {
		return false, nil
	}
	if character == nil || plan.ExpectedSequence < 0 || plan.WeeklyRewardLimit <= 0 || plan.WeeklyRewardLimit > 255 {
		return false, ErrEntryItemConsumeInvalid
	}
	state, err := DimensionHiddenSnapshot(*character, now)
	if err != nil {
		return false, err
	}
	if state.Sequence != plan.ExpectedSequence || state.Sequence == math.MaxInt64 || state.EntriesSinceEncounter == math.MaxInt64 {
		return false, ErrEntryItemConsumeInvalid
	}
	// The configured weekly count is an encounter cap. Recheck it in the
	// entry transaction so a stale party plan cannot admit a third encounter.
	if plan.Encountered && state.WeeklyAppearances >= plan.WeeklyRewardLimit {
		return false, ErrEntryItemConsumeInvalid
	}
	highReward := plan.Encountered && state.WeeklyAppearances < plan.WeeklyRewardLimit
	if plan.Encountered {
		state.EntriesSinceEncounter = 0
		state.WeeklyAppearances++
	} else {
		state.EntriesSinceEncounter++
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats[dimensionHiddenSequenceKey] = state.Sequence + 1
	character.Stats[dimensionHiddenPityKey] = state.EntriesSinceEncounter
	character.Stats[dimensionHiddenWeekKey] = DimensionHiddenWeekID(now)
	character.Stats[dimensionHiddenUsedKey] = state.WeeklyAppearances
	return highReward, nil
}
