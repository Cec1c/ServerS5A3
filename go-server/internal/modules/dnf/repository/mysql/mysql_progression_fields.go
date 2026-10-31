package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (r *characterProgressionScopedCharacterRepository) LoadProgressionCharacter(ctx context.Context, id string) (repository.CharacterRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	return repository.LoadProgressionCharacter(ctx, r.base, id)
}

func (r *characterProgressionScopedSkillRepository) LoadProgressionSkill(ctx context.Context, id string) (repository.SkillRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.SkillRecord{}, false, err
	}
	return repository.LoadProgressionSkill(ctx, r.base, id)
}

func (r *characterProgressionScopedCharacterRepository) SaveProgressionCharacter(ctx context.Context, before, after repository.CharacterRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, before.CharacterID); err != nil {
		return err
	}
	if err := repository.TransactionKeyError(ctx, r.characterID, after.CharacterID); err != nil {
		return err
	}
	return repository.SaveProgressionCharacter(ctx, r.base, before, after)
}

func (s *mysqlCharStore) LoadProgressionCharacter(ctx context.Context, id string) (repository.CharacterRecord, bool, error) {
	table, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return repository.CharacterRecord{}, false, err
	}
	var record repository.CharacterRecord
	var updated sql.NullTime
	values := make([]sql.NullInt64, len(mysqlCharacterStatColumns))
	dest := []any{&record.CharacterID, &record.AccountID, &record.Level}
	for i := range values {
		dest = append(dest, &values[i])
	}
	dest = append(dest, &updated)
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, account_id, level, "+mysqlCharacterStatSelectList()+", updated_at FROM "+table+" WHERE character_id = ?"), id).Scan(dest...)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.CharacterRecord{}, ok, scanErr
	}
	mergeCharacterMirrorStats(&record, values)
	stats, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return repository.CharacterRecord{}, false, err
	}
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery("SELECT stat_key, stat_value FROM "+stats+" WHERE character_id = ? ORDER BY stat_key"), id)
	if err != nil {
		return repository.CharacterRecord{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value int64
		if err := rows.Scan(&key, &value); err != nil {
			return repository.CharacterRecord{}, false, err
		}
		mergeCharacterExtraStat(&record, key, value, characterMirrorStatsAuthoritative())
	}
	if err := rows.Err(); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	if err := rows.Close(); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updated)
	return record, true, nil
}

func (s *mysqlSkillStore) LoadProgressionSkill(ctx context.Context, id string) (repository.SkillRecord, bool, error) {
	table, err := s.router.readTable(mysqlSkillTable, id)
	if err != nil {
		return repository.SkillRecord{}, false, err
	}
	var r repository.SkillRecord
	var updated sql.NullTime
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, total_sp, remaining_sp, total_tp, remaining_tp, synced_level, updated_at FROM "+table+" WHERE character_id = ?"), id).Scan(&r.CharacterID, &r.Points.TotalSP, &r.Points.RemainingSP, &r.Points.TotalTP, &r.Points.RemainingTP, &r.Points.SyncedLevel, &updated)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.SkillRecord{}, ok, scanErr
	}
	r.UpdatedAt = scanTime(updated)
	return r, true, nil
}

func (s *mysqlCharStore) SaveProgressionCharacter(ctx context.Context, before, after repository.CharacterRecord) error {
	// This optional capability is used only by the progression transaction.
	if !s.router.lockReads || before.CharacterID == "" || before.CharacterID != after.CharacterID || before.AccountID != after.AccountID {
		return errors.New("progression delta requires the same locked character")
	}
	id := after.CharacterID
	table, err := s.router.writeTable(mysqlCharTable, id)
	if err != nil {
		return err
	}
	stats, err := s.router.writeTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return err
	}
	sets := []string{"updated_at = ?"}
	args := []any{timeOrNow(after.UpdatedAt, s.router.now)}
	if before.Level != after.Level {
		sets = append(sets, "level = ?")
		args = append(args, after.Level)
	}
	for _, column := range mysqlCharacterStatColumns {
		v, present := after.Stats[column.key]
		old, had := before.Stats[column.key]
		if !present || (had && v == old) {
			continue
		}
		sets = append(sets, quoteSQLIdentifier(column.column)+" = ?")
		args = append(args, v)
	}
	args = append(args, id, after.AccountID)
	if _, err := s.router.db.ExecContext(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE character_id = ? AND account_id = ?", args...); err != nil {
		return err
	}
	for _, key := range sortedStringKeys(after.Stats) {
		value := after.Stats[key]
		if isMySQLCharacterMirrorStatKey(key) {
			continue
		}
		if old, present := before.Stats[key]; present && old == value {
			continue
		}
		query := "INSERT INTO " + stats + " (character_id, stat_key, stat_value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE stat_value = VALUES(stat_value)"
		if _, event := repository.LevelUpEventLevel(key); event {
			query = "INSERT INTO " + stats + " (character_id, stat_key, stat_value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), IF(stat_value < 0 OR VALUES(stat_value) >= 0, stat_value, VALUES(stat_value)))"
		} else if strings.HasPrefix(key, repository.LevelUpRewardStatPrefix) {
			query = "INSERT INTO " + stats + " (character_id, stat_key, stat_value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), stat_value)"
		}
		if _, err := s.router.db.ExecContext(ctx, query, id, key, value); err != nil {
			return err
		}
	}
	// Progression never deletes an audit fact. Non-audit removals are explicit.
	for _, key := range sortedStringKeys(before.Stats) {
		if _, present := after.Stats[key]; present || isMySQLCharacterMirrorStatKey(key) || strings.HasPrefix(key, repository.LevelUpEventStatPrefix) || strings.HasPrefix(key, repository.LevelUpRewardStatPrefix) {
			continue
		}
		if _, err := s.router.db.ExecContext(ctx, "DELETE FROM "+stats+" WHERE character_id = ? AND stat_key = ?", id, key); err != nil {
			return err
		}
	}
	return nil
}
