package progression

import (
	"fmt"
	"math"
)

type ExperienceResult struct {
	PreviousLevel      int
	PreviousExperience uint32
	Gain               uint32
	NewLevel           int
	NewExperience      uint32
	LevelsGained       int
	Saturated          bool
	maximumLevelCap    *maximumLevelExperienceCap
}

// maximumLevelExperienceCap is minted only by the loaded PVF tables. It is
// carried into the commit plan to authorize an exact at-cap normalization,
// while ordinary cumulative-experience regressions remain invalid.
type maximumLevelExperienceCap struct {
	level           int
	entryExperience uint32
}

// LimitCharacterExperienceAtCap bounds the character's cumulative EXP once
// the caller-proven playable cap is reached. Raw Gain and PreviousExperience
// remain intact for HonorExpert accounting and optimistic version checks.
// A short legacy ledger is never inflated to the level-entry threshold.
func (t *Tables) LimitCharacterExperienceAtCap(result ExperienceResult, maximumLevel int) (ExperienceResult, error) {
	if t == nil || maximumLevel <= 0 || result.PreviousLevel <= 0 || result.PreviousLevel > maximumLevel || result.NewLevel < result.PreviousLevel || result.NewLevel > maximumLevel {
		return ExperienceResult{}, ErrLevelOutOfRange
	}
	result.maximumLevelCap = nil
	if result.NewLevel != maximumLevel || result.Gain == 0 {
		return result, nil
	}
	var entry uint32
	if maximumLevel > 1 {
		var err error
		entry, err = t.ThresholdToNext(maximumLevel - 1)
		if err != nil {
			return ExperienceResult{}, err
		}
	}
	if result.NewExperience > entry {
		result.NewExperience = entry
	}
	result.maximumLevelCap = &maximumLevelExperienceCap{level: maximumLevel, entryExperience: entry}
	return result, nil
}

func (cap *maximumLevelExperienceCap) permitsNormalization(expected, next CommitState) bool {
	return cap != nil && cap.level > 0 && expected.Level == cap.level && next.Level == cap.level &&
		expected.Experience > cap.entryExperience && next.Experience == cap.entryExperience
}

// ThresholdToNext returns the cumulative experience required to advance from
// level to level+1. The caller supplies the server's proven level cap when
// applying experience; the threshold-file length is not guessed to be that cap.
func (t *Tables) ThresholdToNext(level int) (uint32, error) {
	if t == nil || level <= 0 || level > len(t.experienceThresholds) {
		return 0, fmt.Errorf("%w: threshold level=%d entries=%d", ErrLevelOutOfRange, level, thresholdCount(t))
	}
	return t.experienceThresholds[level-1], nil
}

// ApplyExperience saturating-adds a current-EXE u32 experience gain and applies
// cumulative PVF thresholds up to maxLevel. At that playable cap only the
// character ledger is bounded; Gain remains available to the HonorExpert owner.
// It does not persist or emit an op37.
func (t *Tables) ApplyExperience(level int, totalExperience, gain uint32, maxLevel int) (ExperienceResult, error) {
	result := ExperienceResult{
		PreviousLevel:      level,
		PreviousExperience: totalExperience,
		Gain:               gain,
		NewLevel:           level,
		NewExperience:      totalExperience,
	}
	if t == nil || level <= 0 || maxLevel <= 0 || level > maxLevel || maxLevel-1 > len(t.experienceThresholds) {
		return ExperienceResult{}, fmt.Errorf("%w: level=%d max=%d thresholds=%d", ErrLevelOutOfRange, level, maxLevel, thresholdCount(t))
	}
	sum := uint64(totalExperience) + uint64(gain)
	if sum > math.MaxUint32 {
		sum = math.MaxUint32
		result.Saturated = true
	}
	result.NewExperience = uint32(sum)
	for result.NewLevel < maxLevel {
		threshold := t.experienceThresholds[result.NewLevel-1]
		if result.NewExperience < threshold {
			break
		}
		result.NewLevel++
	}
	result.LevelsGained = result.NewLevel - result.PreviousLevel
	return t.LimitCharacterExperienceAtCap(result, maxLevel)
}

func thresholdCount(t *Tables) int {
	if t == nil {
		return 0
	}
	return len(t.experienceThresholds)
}
