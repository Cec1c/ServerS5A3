package mysql

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

func (s *mysqlCharStore) ListCharacterIdentities(ctx context.Context, account string, limit int) ([]repository.CharacterRecord, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, db.ErrRecordKeyRequired
	}
	if limit <= 0 {
		limit = repository.DefaultCharacterSlotLimit
	}
	var out []repository.CharacterRecord
	for _, database := range s.router.readDBs {
		if len(out) >= limit {
			break
		}
		table := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		query := s.router.selectQuery("SELECT character_id, account_id, slot FROM " + table + " WHERE account_id = ? AND delete_flag = 0 ORDER BY slot ASC, character_id ASC LIMIT ?")
		rows, err := s.router.db.QueryContext(ctx, query, account, limit-len(out))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row repository.CharacterRecord
			if err := rows.Scan(&row.CharacterID, &row.AccountID, &row.Slot); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
