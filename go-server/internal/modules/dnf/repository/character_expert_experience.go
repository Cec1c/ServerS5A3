package repository

import (
	"context"
	"errors"
)

var ErrCharacterExpertExperience = errors.New("character expert job experience is missing or invalid")

// CharacterExpertExperienceWriter 只更新已存在角色的副职业经验，调用者负责资产事务。
type CharacterExpertExperienceWriter interface {
	SaveExpertJobExperience(context.Context, CharacterRecord) error
}

func SaveCharacterExpertExperience(ctx context.Context, repo CharacterRepository, record CharacterRecord) error {
	value, found := record.Stats["expert_job_exp"]
	if repo == nil || !found || value < 0 || CharacterKey(record) == "" {
		return ErrCharacterExpertExperience
	}
	if writer, ok := repo.(CharacterExpertExperienceWriter); ok {
		return writer.SaveExpertJobExperience(ctx, record)
	}
	// 兼容旧仓储；只将经验合入同事务的新快照，不带入其它过时属性。
	current, found, err := repo.Load(ctx, record.CharacterID)
	if err != nil {
		return err
	}
	if !found {
		return ErrCharacterExpertExperience
	}
	current = CloneCharacter(current)
	if current.Stats == nil {
		current.Stats = make(map[string]int64)
	}
	current.Stats["expert_job_exp"] = value
	current.UpdatedAt = record.UpdatedAt
	return SaveCharacterFields(ctx, repo, current, CharacterFieldStats)
}
