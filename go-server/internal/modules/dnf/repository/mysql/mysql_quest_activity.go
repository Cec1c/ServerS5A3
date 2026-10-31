package mysql

import (
	"context"
	"database/sql"
	"strings"

	"longheng.io/server/internal/platform/db"
)

// 不读取已完成任务的额外字段；字符、任务父行和两个状态投影使用同一次实时查询。
func (s *mysqlQuestStore) LoadCharacterQuestStatuses(ctx context.Context, characterID string) ([]string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return nil, false, db.ErrRecordKeyRequired
	}
	characters, err := s.router.readTable(mysqlCharTable, characterID)
	if err != nil {
		return nil, false, err
	}
	quests, err := s.router.readTable(mysqlQuestTable, characterID)
	if err != nil {
		return nil, false, err
	}
	states, err := s.router.readTable(mysqlQuestStatesTable, characterID)
	if err != nil {
		return nil, false, err
	}
	// BINARY避免active与带重音/不同大小写的状态被不区分大小写的校对合并，
	// 实际资格仍由Go沿原规则检查。父任务缺失时忽略孤立状态行，与完整Load一致。
	query := "SELECT c.character_id, CAST(s.status AS BINARY) FROM " + characters + " c LEFT JOIN " + quests +
		" q ON q.character_id = c.character_id LEFT JOIN " + states +
		" s ON s.character_id = q.character_id AND s.state_group IN ('states', 'progress')" +
		" WHERE c.character_id = ? GROUP BY c.character_id, CAST(s.status AS BINARY)"
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), characterID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var statuses []string
	found := false
	for rows.Next() {
		var storedID string
		var status sql.NullString
		if err := rows.Scan(&storedID, &status); err != nil {
			return nil, false, err
		}
		if storedID != characterID {
			return nil, false, nil
		}
		found = true
		if status.Valid {
			statuses = append(statuses, status.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	return statuses, found, nil
}
