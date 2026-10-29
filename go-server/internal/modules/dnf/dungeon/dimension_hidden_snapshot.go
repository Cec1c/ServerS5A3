package dungeon

import (
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
