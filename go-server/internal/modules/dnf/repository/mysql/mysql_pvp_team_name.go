package mysql

import (
	"context"
	"database/sql"
	"errors"
)

func (s *mysqlAccountStore) FindAccountIDByPVPTotalTeamName(ctx context.Context, name string) (string, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return "", false, err
	}
	for _, database := range s.router.readDBs {
		table := mysqlTable(database, s.router.tablePrefix, mysqlAccountMetadataTable)
		query := "SELECT account_id FROM " + table + " WHERE entry_key = ? AND LOWER(JSON_UNQUOTE(JSON_EXTRACT(entry_value, '$.name'))) = LOWER(?) LIMIT 1"
		var accountID string
		err := s.router.db.QueryRowContext(ctx, query, "pvp_total_match_team_v1", name).Scan(&accountID)
		if err == nil {
			return accountID, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}
	return "", false, nil
}
