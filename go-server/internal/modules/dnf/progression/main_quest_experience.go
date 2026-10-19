package progression

import (
	"errors"
	"fmt"
	"math"
)

var ErrQuestExperiencePercent = errors.New("dnf progression quest experience percent is invalid")

// MainQuestExperiencePercent returns the configured percentage of the
// cumulative experience interval for questLevel. ExpTable entries are cumulative
// thresholds, so the interval is T(level)-T(level-1), with T(0)=0.
func (t *Tables) MainQuestExperiencePercent(questLevel, percent int) (uint32, error) {
	if percent < 0 || percent > 100 {
		return 0, fmt.Errorf("%w: percent=%d", ErrQuestExperiencePercent, percent)
	}
	if percent == 0 {
		return 0, nil
	}
	if t == nil || questLevel <= 0 || questLevel > len(t.experienceThresholds) {
		return 0, fmt.Errorf(
			"%w: quest level=%d entries=%d",
			ErrLevelOutOfRange,
			questLevel,
			thresholdCount(t),
		)
	}
	var previous uint64
	if questLevel > 1 {
		previous = uint64(t.experienceThresholds[questLevel-2])
	}
	current := uint64(t.experienceThresholds[questLevel-1])
	if current <= previous {
		return 0, fmt.Errorf(
			"%w: %s cumulative threshold[%d]=%d previous=%d",
			ErrTableMalformed,
			ExperienceTablePath,
			questLevel,
			current,
			previous,
		)
	}
	value := (current - previous) * uint64(percent) / 100
	if value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: quest experience=%d", ErrExperienceOutOfRange, value)
	}
	return uint32(value), nil
}
