package mysql

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

func characterStatWriteSuffix(key string, delta bool) string {
	if _, event := r.LevelUpEventLevel(key); event {
		return " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), IF(stat_value < 0 OR VALUES(stat_value) >= 0, stat_value, VALUES(stat_value)))"
	}
	if strings.HasPrefix(key, r.LevelUpRewardStatPrefix) {
		return " ON DUPLICATE KEY UPDATE stat_value = IF(stat_value = 0, VALUES(stat_value), stat_value)"
	}
	if delta {
		return " ON DUPLICATE KEY UPDATE stat_value = VALUES(stat_value)"
	}
	return ""
}

// Consecutive equal conflict rules share a batch. Key order and permanent
// audit merge rules are preserved, including in the packet-size fallback.
func writeCharacterStatRows(ctx context.Context, db SQLDB, table, id string, keys []string, stats map[string]int64, delta bool) error {
	var batch *rowInsertBatcher
	for _, key := range keys {
		suffix := characterStatWriteSuffix(key, delta)
		if batch == nil || batch.shape.suffix != suffix {
			if batch != nil {
				if err := batch.flush(); err != nil {
					return err
				}
			}
			batch = newRowInsertBatcher(ctx, db, id, insertShape{table: table, collection: "character_stats", prefix: "INSERT INTO " + table + " (character_id, stat_key, stat_value) VALUES ", placeholders: "(?, ?, ?)", columns: 3, suffix: suffix})
		}
		if err := batch.add(id, key, stats[key]); err != nil {
			return err
		}
	}
	if batch != nil {
		return batch.flush()
	}
	return nil
}
