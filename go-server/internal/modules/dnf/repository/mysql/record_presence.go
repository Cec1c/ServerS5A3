// 本文件实现 DNF 仓储权威父记录的存在性读取。
package mysql

import (
	"context"
	"database/sql"
	"errors"

	"longheng.io/server/internal/modules/dnf/repository"
)

var (
	_ repository.RecordPresenceReader = (*mysqlBagStore)(nil)
	_ repository.RecordPresenceReader = (*mysqlEquipmentStore)(nil)
	_ repository.RecordPresenceReader = (*mysqlSkillStore)(nil)
	_ repository.RecordPresenceReader = (*mysqlSettingsStore)(nil)
)

// Exists 只检查角色背包父记录是否存在。
func (s *mysqlBagStore) Exists(ctx context.Context, characterID string) (bool, error) {
	return mysqlParentRecordExists(ctx, s.router, mysqlBagTable, "character_id", characterID)
}

// Exists 只检查角色穿戴装备父记录是否存在。
func (s *mysqlEquipmentStore) Exists(ctx context.Context, characterID string) (bool, error) {
	return mysqlParentRecordExists(ctx, s.router, mysqlEquipmentTable, "character_id", characterID)
}

// Exists 只检查角色技能父记录是否存在。
func (s *mysqlSkillStore) Exists(ctx context.Context, characterID string) (bool, error) {
	return mysqlParentRecordExists(ctx, s.router, mysqlSkillTable, "character_id", characterID)
}

// Exists 只检查设置父记录是否存在。
func (s *mysqlSettingsStore) Exists(ctx context.Context, scope string) (bool, error) {
	return mysqlParentRecordExists(ctx, s.router, mysqlSettingsTable, "scope", scope)
}

func mysqlParentRecordExists(ctx context.Context, router mysqlRouter, tableSuffix, keyColumn, key string) (bool, error) {
	table, err := router.readTable(tableSuffix, key)
	if err != nil {
		return false, err
	}
	query := router.selectQuery("SELECT 1 FROM " + table + " WHERE " + keyColumn + " = ?")
	var present int
	if err := router.db.QueryRowContext(ctx, query, key).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
