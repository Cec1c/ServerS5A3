package dungeon

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"time"
)

type DimensionEntranceCount struct {
	BaseUsed          uint32
	ExtraAvailable    uint32
	WeeklyAppearances byte
}

// DimensionGateEntranceProjection translates durable allowance into the native
// 812 fields: base used, extra still available, capped weekly appearances.
func DimensionGateEntranceProjection(character dnfrepo.CharacterRecord, now time.Time, base, extraLimit, weeklyLimit int64) (DimensionEntranceCount, error) {
	if base > math.MaxUint32 || extraLimit > math.MaxUint32 || weeklyLimit <= 0 || weeklyLimit > 255 {
		return DimensionEntranceCount{}, ErrEntryItemConsumeInvalid
	}
	used, granted, total, err := DimensionGateDailyAllowance(character, now, base, extraLimit)
	if err != nil {
		return DimensionEntranceCount{}, err
	}
	if used > total {
		return DimensionEntranceCount{}, ErrEntryItemConsumeInvalid
	}
	state, err := DimensionHiddenSnapshot(character, now)
	if err != nil {
		return DimensionEntranceCount{}, err
	}
	baseUsed := used
	if baseUsed > base {
		baseUsed = base
	}
	extraAvailable := granted - (used - baseUsed)
	weekly := state.WeeklyAppearances
	if weekly > weeklyLimit {
		weekly = weeklyLimit
	}
	return DimensionEntranceCount{uint32(baseUsed), uint32(extraAvailable), byte(weekly)}, nil
}
