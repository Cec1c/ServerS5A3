package quest

import (
	"errors"
	"fmt"
	"math"
)

const (
	mainQuestExperienceMultiplierMin = 1
	mainQuestExperienceMultiplierMax = 100
)

var (
	ErrFinishMainQuestExperienceMultiplierInvalid = errors.New("dnf quest finish main quest experience multiplier is invalid")
	ErrFinishMainQuestExperienceOverflow          = errors.New("dnf quest finish main quest experience overflows uint32")
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
