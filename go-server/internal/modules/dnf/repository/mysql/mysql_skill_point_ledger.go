package mysql

import (
	"context"
	"database/sql"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlSkillStore) LoadPointLedger(ctx context.Context, characterID string) (repository.SkillPointLedger, bool, error) {
	table, err := s.router.readTable(mysqlSkillTable, characterID)
	if err != nil {
		return repository.SkillPointLedger{}, false, err
	}
	query := s.router.selectQuery("SELECT character_id, total_sp, remaining_sp, total_tp, remaining_tp, synced_level, updated_at FROM " + table + " WHERE character_id = ?")
	var record repository.SkillPointLedger
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, characterID).Scan(&record.CharacterID,
		&record.Points.TotalSP, &record.Points.RemainingSP, &record.Points.TotalTP, &record.Points.RemainingTP, &record.Points.SyncedLevel, &updatedAt)
	if err != nil {
		found, scanErr := scanErr(err)
		return repository.SkillPointLedger{}, found, scanErr
	}
	// 保留完整读取原有的技能页头锁读、扫描和有效页码校验。
	if _, _, err := s.loadSkillTreeProfile(ctx, characterID); err != nil {
		return repository.SkillPointLedger{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	return record, true, nil
}

func (s *mysqlSkillStore) SavePointLedger(ctx context.Context, record repository.SkillPointLedger) error {
	return s.SaveFields(ctx, repository.SkillRecord{CharacterID: record.CharacterID, Points: record.Points, UpdatedAt: record.UpdatedAt}, repository.SkillFieldPoints)
}

// 仅在底层明确提供成对的窄读/窄写时增加该能力，保留原完整仓储接口。
func scopeSkillPointLedger(scoped, base repository.SkillRepository, characterID string) repository.SkillRepository {
	points, ok := base.(repository.SkillPointLedgerRepository)
	if !ok {
		return scoped
	}
	return &scopedSkillPointLedger{SkillRepository: scoped, points: points, characterID: characterID}
}

type scopedSkillPointLedger struct {
	repository.SkillRepository
	points      repository.SkillPointLedgerRepository
	characterID string
}

func (r *scopedSkillPointLedger) LoadPointLedger(ctx context.Context, id string) (repository.SkillPointLedger, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.SkillPointLedger{}, false, err
	}
	return r.points.LoadPointLedger(ctx, id)
}

func (r *scopedSkillPointLedger) SavePointLedger(ctx context.Context, record repository.SkillPointLedger) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.points.SavePointLedger(ctx, record)
}

func (r *scopedSkillPointLedger) SaveFields(ctx context.Context, record repository.SkillRecord, fields ...repository.SkillField) error {
	return repository.SaveSkillFields(ctx, r.SkillRepository, record, fields...)
}
