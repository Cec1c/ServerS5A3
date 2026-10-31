package mysql

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 仅由结算事务中成功完整锁读的快照调用。不跨事务保留资格或资产状态。
func saveChangedAccountMetadata(ctx context.Context, database SQLDB, table, id string, before, after map[string]string) error {
	for _, key := range sortedStringKeys(before) {
		if _, exists := after[key]; !exists {
			if _, err := database.ExecContext(ctx, "DELETE FROM "+table+" WHERE account_id = ? AND entry_key = ?", id, key); err != nil {
				return err
			}
		}
	}
	query := buildUpsert(table, []string{"account_id", "entry_key", "entry_value"}, []string{updateValue("entry_value")})
	for _, key := range sortedStringKeys(after) {
		if value, exists := before[key]; exists && value == after[key] {
			continue
		}
		if _, err := database.ExecContext(ctx, query, id, key, after[key]); err != nil {
			return err
		}
	}
	return nil
}

func permanentCharacterStat(key string) bool {
	return strings.HasPrefix(key, repository.LevelUpEventStatPrefix) || strings.HasPrefix(key, repository.LevelUpRewardStatPrefix)
}

func characterStatSnapshotEligible(stats map[string]int64) bool {
	for key := range stats {
		if strings.HasPrefix(key, repository.LevelUpEventStatPrefix) {
			if _, valid := repository.LevelUpEventLevel(key); !valid {
				return false
			}
		}
	}
	return true
}

func saveChangedCharacterStats(ctx context.Context, database SQLDB, table, id string, before, after map[string]int64) error {
	for _, key := range sortedStringKeys(before) {
		if isMySQLCharacterMirrorStatKey(key) || permanentCharacterStat(key) {
			continue
		}
		if _, exists := after[key]; !exists {
			if _, err := database.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND stat_key = ?", id, key); err != nil {
				return err
			}
		}
	}
	for _, key := range sortedStringKeys(after) {
		if isMySQLCharacterMirrorStatKey(key) {
			continue
		}
		if value, exists := before[key]; exists && value == after[key] {
			continue
		}
		query := "INSERT INTO " + table + " (character_id, stat_key, stat_value) VALUES (?, ?, ?)"
		if _, event := repository.LevelUpEventLevel(key); event {
			query += " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), IF(stat_value < 0 OR VALUES(stat_value) >= 0, stat_value, VALUES(stat_value)))"
		} else if strings.HasPrefix(key, repository.LevelUpRewardStatPrefix) {
			query += " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), stat_value)"
		} else {
			query += " ON DUPLICATE KEY UPDATE stat_value = VALUES(stat_value)"
		}
		if _, err := database.ExecContext(ctx, query, id, key, after[key]); err != nil {
			return err
		}
	}
	return nil
}
