package dnfbridge

import (
	"errors"
	"fmt"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentHonorExpertLevelStatKey              = dnfhonor.ExpertLevelStatKey
	currentHonorExpertProgressExperienceStatKey = dnfhonor.ExpertProgressExperienceStatKey
)

var errCurrentHonorExpertStateInvalid = errors.New("current HonorExpert persisted state is invalid")

// currentHonorExpertProgress returns the character-scoped state shared by the
// current mode-1, actor-tail, and op37 readers. Missing legacy stats are the
// valid level-zero challenger state.
func currentHonorExpertProgress(character dnfrepo.CharacterRecord) (dnfhonor.ExpertProgress, error) {
	progress, err := dnfhonor.ExpertProgressFromStats(character.Stats)
	if err != nil {
		return dnfhonor.ExpertProgress{}, fmt.Errorf("%w: character=%s: %v", errCurrentHonorExpertStateInvalid, character.CharacterID, err)
	}
	return progress, nil
}

func currentHonorExpertStats(progress dnfhonor.ExpertProgress) map[string]int64 {
	return map[string]int64{
		currentHonorExpertLevelStatKey:              int64(progress.Level),
		currentHonorExpertProgressExperienceStatKey: int64(progress.CurrentLevelExperience),
	}
}

// currentHonorExpertExperienceGain isolates only the part of a proven
// character-EXP award earned after the DNF90 level cap. The entry threshold
// comes from the runtime progression PVF, while the cap comes from the
// current EXE/profile compatibility unit.
func currentHonorExpertExperienceGain(
	tables *progression.Tables,
	previousLevel int,
	previousExperience uint32,
	gain uint32,
) (uint32, error) {
	if previousLevel <= 0 || previousLevel > currentAdventureCharacterLevelCap {
		return 0, fmt.Errorf(
			"%w: level=%d cap=%d",
			errCurrentHonorExpertStateInvalid,
			previousLevel,
			currentAdventureCharacterLevelCap,
		)
	}
	if gain == 0 || previousLevel < currentAdventureCharacterLevelCap-1 {
		return 0, nil
	}
	if previousLevel >= currentAdventureCharacterLevelCap {
		return gain, nil
	}
	if tables == nil {
		return 0, errCurrentHonorExpertStateInvalid
	}
	entryExperience, err := tables.ThresholdToNext(currentAdventureCharacterLevelCap - 1)
	if err != nil {
		return 0, err
	}
	return dnfhonor.CalculateHonorExperienceGain(
		previousLevel,
		previousExperience,
		gain,
		dnfhonor.CharacterExperienceCap{
			MaxLevel:                currentAdventureCharacterLevelCap,
			MaxLevelEntryExperience: entryExperience,
		},
	)
}

func planCurrentHonorExpertProgress(
	tables *dnfhonor.Tables,
	character dnfrepo.CharacterRecord,
	gain uint32,
) (dnfhonor.ExpertProgress, error) {
	if tables == nil {
		return dnfhonor.ExpertProgress{}, errHonorTableUnavailable
	}
	current, err := currentHonorExpertProgress(character)
	if err != nil {
		return dnfhonor.ExpertProgress{}, err
	}
	return tables.AdvanceExpert(current, uint64(gain))
}

// currentAdventureInfoRosterHonorLevel is the per-character 名誉等级 value the
// adventure-group character card displays. The persisted character-scoped
// honour level is only earned after the DNF90 level cap, so a character that
// never reached the cap reports 0 regardless of any stale stored stat: an
// under-cap card must not show the honour badge that only max-level characters
// can earn. A corrupted persisted value projects neutral for the same reason
// writeCurrentHonorExpertState does.
func currentAdventureInfoRosterHonorLevel(character dnfrepo.CharacterRecord) uint32 {
	if character.Level < currentAdventureCharacterLevelCap {
		return 0
	}
	progress, err := currentHonorExpertProgress(character)
	if err != nil {
		return 0
	}
	return progress.Level
}

// writeCurrentHonorExpertState writes the current EXE's shared
// {u32 level, u64 current-level EXP} actor state. A corrupted persisted value
// is not allowed to desynchronize packet readers, so it projects neutral until
// the durable record can be repaired through an authoritative award.
func writeCurrentHonorExpertState(writer *packetWriter, character dnfrepo.CharacterRecord) {
	progress, err := currentHonorExpertProgress(character)
	if err != nil {
		writer.writeUint32(0)
		writer.writeZero(8)
		return
	}
	writer.writeUint32(progress.Level)
	writer.writeUint64(progress.CurrentLevelExperience)
}
