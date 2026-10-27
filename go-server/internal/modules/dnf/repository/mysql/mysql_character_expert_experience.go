package mysql

import (
	"context"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 经验是角色权威列；提取不需要逐条重写额外属性或其他权威列。
func (s *mysqlCharStore) SaveExpertJobExperience(ctx context.Context, record repository.CharacterRecord) error {
	characterID, err := requireRecordKey(repository.CharacterKey, record, "character")
	if err != nil {
		return err
	}
	value, found := record.Stats["expert_job_exp"]
	if !found || value < 0 {
		return repository.ErrCharacterExpertExperience
	}
	table, err := s.router.writeTable(mysqlCharTable, characterID)
	if err != nil {
		return err
	}
	_, err = s.router.db.ExecContext(ctx, "UPDATE "+table+" SET expert_job_exp = ?, updated_at = ? WHERE character_id = ?", value, timeOrNow(record.UpdatedAt, s.router.now), characterID)
	return err
}
