package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

func (s *mysqlCharStore) ListPendingLevelUpCharacters(ctx context.Context, after string, limit int) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 128 {
		limit = 128
	}
	ids := make(map[string]bool)
	// 从写库读取待消费事实，避免读副本延迟被误判为没有事件。
	for _, database := range s.router.writeDBs {
		stats := mysqlTable(database, s.router.tablePrefix, mysqlCharacterStatsTable)
		characters := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		rows, err := s.router.db.QueryContext(ctx, "SELECT DISTINCT s.character_id FROM "+stats+" s INNER JOIN "+characters+" c ON c.character_id = s.character_id WHERE LEFT(s.stat_key, ?) = ? AND s.stat_value > 0 AND c.delete_flag = 0 AND s.character_id > ? ORDER BY s.character_id LIMIT ?", len(repository.LevelUpEventStatPrefix), repository.LevelUpEventStatPrefix, after, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids[id] = true
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
