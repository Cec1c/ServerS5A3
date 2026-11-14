package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
	"strings"
)

func (s *mysqlCharStore) ListAccountCharacterLevels(ctx context.Context, account string, limit int) ([]repository.AccountCharacterLevel, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, db.ErrRecordKeyRequired
	}
	if limit <= 0 {
		limit = repository.DefaultCharacterSlotLimit
	}
	result := make([]repository.AccountCharacterLevel, 0, limit)
	// Match ListByAccount's shard and slot ordering, without loading each
	// character's stats, equipment, roster and location.
	for _, database := range s.router.readDBs {
		if len(result) >= limit {
			break
		}
		table := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		query := "SELECT level FROM " + table + " WHERE account_id = ? AND delete_flag = 0 ORDER BY slot ASC, character_id ASC LIMIT ?"
		rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), account, limit-len(result))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row repository.AccountCharacterLevel
			if err := rows.Scan(&row.Level); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, row)
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
	return result, nil
}

func (s *mysqlAccountStore) LoadAccountHonorExperience(ctx context.Context, account string) (uint64, bool, error) {
	table, err := s.router.readTable(mysqlAccountTable, account)
	if err != nil {
		return 0, false, err
	}
	var value uint64
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT honor_exp FROM "+table+" WHERE account_id = ?"), account).Scan(&value)
	if err != nil {
		found, err := scanErr(err)
		return 0, found, err
	}
	return value, true, nil
}
