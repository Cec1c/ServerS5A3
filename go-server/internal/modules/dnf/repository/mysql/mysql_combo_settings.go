package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
)

// 连招配置只允许访问当前角色的独立命名空间。
type characterComboSettingsRepository struct {
	characterID string
	base        repository.SettingsRepository
}

func (r *characterComboSettingsRepository) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if r == nil || r.base == nil || !(repository.CharacterSkillSettingsScope(r.characterID, scope) || repository.QuestMaterialSettingsScope(r.characterID, scope)) {
		return repository.SettingsRecord{}, false, repository.ErrCharacterSettlementTransactionUnavailable
	}
	return r.base.Load(ctx, scope)
}
func (r *characterComboSettingsRepository) Save(ctx context.Context, record repository.SettingsRecord) error {
	if r == nil || r.base == nil || !(repository.CharacterSkillSettingsScope(r.characterID, repository.SettingsKey(record)) || repository.QuestMaterialSettingsScope(r.characterID, repository.SettingsKey(record))) {
		return repository.ErrCharacterSettlementTransactionUnavailable
	}
	return r.base.Save(ctx, record)
}
