package mysql

import (
	"context"
	"database/sql"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

func (s *mysqlAccountStore) SaveMetadataEntryWithPrefixLimit(ctx context.Context, accountID, prefix, key, activeValue string, limit int, updatedAt time.Time) error {
	if _, ok := s.router.db.(sqlTxBeginner); !ok {
		return repository.ErrAccountMetadataUnavailable
	}
	if strings.TrimSpace(accountID) == "" || prefix == "" || !strings.HasPrefix(key, prefix) || activeValue == "" || limit <= 0 {
		return repository.ErrAccountMetadataKeyRequired
	}
	table, err := s.router.writeTable(mysqlAccountTable, accountID)
	if err != nil {
		return err
	}
	metadataTable, err := s.router.writeTable(mysqlAccountMetadataTable, accountID)
	if err != nil {
		return err
	}
	updatedAt = timeOrNow(updatedAt, s.router.now)
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		// Lock the account owner before counting. SaveMetadataEntry also updates
		// this row first, so deletes and other metadata writes share this lock.
		var owner string
		if err := database.QueryRowContext(ctx, "SELECT account_id FROM "+table+" WHERE account_id = ? FOR UPDATE", accountID).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return repository.ErrAccountMetadataUnavailable
			}
			return err
		}
		var current string
		err := database.QueryRowContext(ctx, "SELECT entry_value FROM "+metadataTable+" WHERE account_id = ? AND entry_key = ?", accountID, key).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != activeValue {
			var count int
			if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+metadataTable+" WHERE account_id = ? AND LEFT(entry_key, ?) = ? AND entry_value = ?", accountID, len(prefix), prefix, activeValue).Scan(&count); err != nil {
				return err
			}
			if count >= limit {
				return repository.ErrAccountMetadataLimitReached
			}
		}
		if _, err := database.ExecContext(ctx, "UPDATE "+table+" SET updated_at = ? WHERE account_id = ?", updatedAt, accountID); err != nil {
			return err
		}
		_, err = database.ExecContext(ctx, buildUpsert(metadataTable, []string{"account_id", "entry_key", "entry_value"}, []string{updateValue("entry_value")}), accountID, key, activeValue)
		return err
	})
}
