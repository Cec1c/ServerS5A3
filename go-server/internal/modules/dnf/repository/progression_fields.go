package repository

import "context"

// ProgressionCharacterReader omits presentation/location data. The returned
// record is only suitable for progression, never for a full-record save.
type ProgressionCharacterReader interface {
	LoadProgressionCharacter(context.Context, string) (CharacterRecord, bool, error)
}

type ProgressionSkillReader interface {
	LoadProgressionSkill(context.Context, string) (SkillRecord, bool, error)
}

// ProgressionCharacterWriter requires before to be read under the same
// transaction lock. It preserves all fields outside this transition.
type ProgressionCharacterWriter interface {
	SaveProgressionCharacter(context.Context, CharacterRecord, CharacterRecord) error
}

func LoadProgressionCharacter(ctx context.Context, r CharacterRepository, id string) (CharacterRecord, bool, error) {
	_, canWrite := r.(ProgressionCharacterWriter)
	if narrow, ok := r.(ProgressionCharacterReader); ok && canWrite {
		return narrow.LoadProgressionCharacter(ctx, id)
	}
	return r.Load(ctx, id)
}

func LoadProgressionSkill(ctx context.Context, r SkillRepository, id string) (SkillRecord, bool, error) {
	_, canWriteFields := r.(interface {
		SaveFields(context.Context, SkillRecord, ...SkillField) error
	})
	if narrow, ok := r.(ProgressionSkillReader); ok && canWriteFields {
		return narrow.LoadProgressionSkill(ctx, id)
	}
	return r.Load(ctx, id)
}

func SaveProgressionCharacter(ctx context.Context, r CharacterRepository, before, after CharacterRecord) error {
	if narrow, ok := r.(ProgressionCharacterWriter); ok {
		return narrow.SaveProgressionCharacter(ctx, before, after)
	}
	return SaveCharacterFields(ctx, r, after, CharacterFieldBase, CharacterFieldStats)
}
