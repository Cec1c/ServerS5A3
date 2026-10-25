package dungeon

import (
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	dimensionHiddenSequenceKey = "dimension_hidden_entry_sequence"
	dimensionHiddenPityKey     = "dimension_hidden_entries_since_encounter"
	dimensionHiddenWeekKey     = "dimension_hidden_reward_week"
	dimensionHiddenUsedKey     = "dimension_hidden_weekly_appearances"
)

// DimensionHiddenWeekID follows the published dimension update's Thursday
// 06:00 China reset. Service day zero (1970-01-01) is a Thursday.
// https://dnf.qq.com/cp/a20180524version/page4.shtml
func DimensionHiddenWeekID(now time.Time) int64 {
	day := premium.ServiceDayID(now)
	return day - (day%7+7)%7
}

type DimensionHiddenState struct {
	Sequence              int64
	EntriesSinceEncounter int64
	WeeklyAppearances     int64
}

// DimensionHiddenSnapshot is a read-only projection. Daily/weekly resets must
// not erase the character's accumulated encounter probability.
func DimensionHiddenSnapshot(character dnfrepo.CharacterRecord, now time.Time) (DimensionHiddenState, error) {
	if now.IsZero() {
		return DimensionHiddenState{}, ErrEntryItemConsumeInvalid
	}
	state := DimensionHiddenState{
		Sequence:              character.Stats[dimensionHiddenSequenceKey],
		EntriesSinceEncounter: character.Stats[dimensionHiddenPityKey],
	}
	if character.Stats[dimensionHiddenWeekKey] == DimensionHiddenWeekID(now) {
		state.WeeklyAppearances = character.Stats[dimensionHiddenUsedKey]
	}
	if state.Sequence < 0 || state.EntriesSinceEncounter < 0 || state.WeeklyAppearances < 0 {
		return DimensionHiddenState{}, ErrEntryItemConsumeInvalid
	}
	return state, nil
}

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
	highReward := plan.Encountered && state.WeeklyAppearances < plan.WeeklyRewardLimit
	if plan.Encountered {
		state.EntriesSinceEncounter = 0
		// Only the highest-reward allowance is required by the client. Capping
		// this counter does not disable later appearances or reset probability.
		if state.WeeklyAppearances < plan.WeeklyRewardLimit {
			state.WeeklyAppearances++
		}
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
