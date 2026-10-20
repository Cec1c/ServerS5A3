package quest

import (
	"errors"
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/progression"
)

const (
	mainQuestExperienceMultiplierMin = 1
	mainQuestExperienceMultiplierMax = 100
	mainQuestExperiencePercentMin    = 0
	mainQuestExperiencePercentMax    = 100
)

var (
	ErrFinishMainQuestExperienceMultiplierInvalid   = errors.New("dnf quest finish main quest experience multiplier is invalid")
	ErrFinishMainQuestExperienceOverflow            = errors.New("dnf quest finish main quest experience overflows uint32")
	ErrFinishMainQuestExperiencePercentInvalid      = errors.New("dnf quest finish main quest experience percent is invalid")
	ErrFinishMainQuestExperiencePercentLevelInvalid = errors.New("dnf quest finish main quest experience level is invalid")
)

func normalizeMainQuestExperienceMultiplier(value int) (int, error) {
	if value == 0 {
		return 1, nil
	}
	if value < mainQuestExperienceMultiplierMin || value > mainQuestExperienceMultiplierMax {
		return 0, fmt.Errorf("%w: multiplier=%d", ErrFinishMainQuestExperienceMultiplierInvalid, value)
	}
	return value, nil
}

func normalizeMainQuestExperiencePercent(value int) (int, error) {
	if value < mainQuestExperiencePercentMin || value > mainQuestExperiencePercentMax {
		return 0, fmt.Errorf("%w: percent=%d", ErrFinishMainQuestExperiencePercentInvalid, value)
	}
	return value, nil
}

// isPercentMainQuestExperienceDefinition selects only ordinary standalone epic
// main quests. Job changes and the level-ignore exceptions retain the legacy
// reward path, as do epic children.
func isPercentMainQuestExperienceDefinition(definition Definition) bool {
	return normalizeQuestTag(definition.Grade) == "epic" &&
		definition.MainQuestID <= 0 &&
		definition.JobChangeQuest == 0 &&
		!definition.IgnoreQuestLevel4Exp
}

func isMainQuestExperienceDefinition(catalog *Catalog, definition Definition) bool {
	if normalizeQuestTag(definition.Grade) == "epic" {
		return true
	}
	if definition.MainQuestID <= 0 || catalog == nil {
		return false
	}
	parent, found := catalog.Find(definition.MainQuestID)
	return found && normalizeQuestTag(parent.Grade) == "epic"
}

func applyMainQuestExperienceMultiplier(
	baseExperienceDelta uint32,
	experienceBonusDelta uint32,
	experienceDelta uint32,
	multiplier int,
) (uint32, uint32, uint32, error) {
	normalized, err := normalizeMainQuestExperienceMultiplier(multiplier)
	if err != nil {
		return 0, 0, 0, err
	}
	if normalized == 1 {
		return baseExperienceDelta, experienceBonusDelta, experienceDelta, nil
	}
	scale := func(value uint32) (uint32, error) {
		scaled := uint64(value) * uint64(normalized)
		if scaled > math.MaxUint32 {
			return 0, fmt.Errorf(
				"%w: value=%d multiplier=%d",
				ErrFinishMainQuestExperienceOverflow,
				value,
				normalized,
			)
		}
		return uint32(scaled), nil
	}
	base, err := scale(baseExperienceDelta)
	if err != nil {
		return 0, 0, 0, err
	}
	bonus, err := scale(experienceBonusDelta)
	if err != nil {
		return 0, 0, 0, err
	}
	total, err := scale(experienceDelta)
	if err != nil {
		return 0, 0, 0, err
	}
	return base, bonus, total, nil
}

func finishMainQuestExperiencePercent(
	tables *progression.Tables,
	definition Definition,
	percent int,
) (uint32, error) {
	if !definition.HasLevel || definition.LevelMin <= 0 || definition.LevelMax < definition.LevelMin {
		return 0, fmt.Errorf(
			"%w: quest=%d has_level=%t min=%d max=%d",
			ErrFinishMainQuestExperiencePercentLevelInvalid,
			definition.ID,
			definition.HasLevel,
			definition.LevelMin,
			definition.LevelMax,
		)
	}
	value, err := tables.MainQuestExperiencePercent(definition.LevelMin, percent)
	if err != nil {
		return 0, fmt.Errorf(
			"%w: quest=%d level=%d: %v",
			ErrFinishMainQuestExperiencePercentLevelInvalid,
			definition.ID,
			definition.LevelMin,
			err,
		)
	}
	return value, nil
}
