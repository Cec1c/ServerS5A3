package mysql

import (
	"context"
	"database/sql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlCharStore) LoadCharacterWedding(ctx context.Context, id string) (r.CharacterWeddingSnapshot, bool, error) {
	characters, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	stats, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	query := "SELECT c.character_id, c.account_id, s.stat_key, s.stat_value FROM " + characters + " c LEFT JOIN " + stats + " s ON s.character_id = c.character_id AND s.stat_key LIKE ? ESCAPE '=' WHERE c.character_id = ? ORDER BY s.stat_key"
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), "wedding=_v1=_%", id)
	if err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	defer rows.Close()
	result := r.CharacterWeddingSnapshot{Stats: map[string]int64{}}
	found := false
	for rows.Next() {
		var key sql.NullString
		var value sql.NullInt64
		if err := rows.Scan(&result.CharacterID, &result.AccountID, &key, &value); err != nil {
			return r.CharacterWeddingSnapshot{}, false, err
		}
		found = true
		if key.Valid && value.Valid {
			result.Stats[key.String] = value.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	if err := rows.Close(); err != nil {
		return r.CharacterWeddingSnapshot{}, false, err
	}
	return result, found, nil
}
