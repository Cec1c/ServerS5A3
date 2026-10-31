package mysql

import (
	"context"
	"database/sql"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

func (s *mysqlQuestStore) LoadCharacterQuestActivities(ctx context.Context, characterID string) ([]repository.QuestActivity, bool, error) {
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
	// 仅以二进制排除确切的completed历史状态；其余状态仍由Go处理大小写/Unicode空白。
	// 两个投影只读编号/状态，不读取触发值、Extra或奖励等完整记录。
	query := "SELECT c.character_id, s.quest_id, CAST(s.status AS BINARY) FROM " + characters + " c LEFT JOIN " + quests +
		" q ON q.character_id = c.character_id LEFT JOIN " + states +
		" s ON s.character_id = q.character_id AND s.state_group IN ('states', 'progress')" +
		" AND CAST(s.status AS BINARY) <> CAST('completed' AS BINARY) WHERE c.character_id = ?"
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), characterID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var activities []repository.QuestActivity
	found := false
	for rows.Next() {
		var storedID string
		var questID sql.NullInt64
		var status sql.NullString
		if err := rows.Scan(&storedID, &questID, &status); err != nil {
			return nil, false, err
		}
		if storedID != characterID {
			return nil, false, nil
		}
		found = true
		if questID.Valid && status.Valid {
			activities = append(activities, repository.QuestActivity{QuestID: questID.Int64, Status: status.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	return activities, found, nil
}
