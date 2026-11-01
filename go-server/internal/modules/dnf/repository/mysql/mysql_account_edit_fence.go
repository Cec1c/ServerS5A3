package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.AccountEditFence = (*mysqlAccountStore)(nil)

func (s *mysqlAccountStore) WaitForAccountEdits(ctx context.Context, id string) error {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return errors.New("invalid account edit fence identity")
	}
	beginner, ok := s.router.db.(sqlTxBeginner)
	if !ok {
		return errors.New("account edit fence transaction unavailable")
	}
	table, err := s.router.writeTable(mysqlAccountTable, id)
	if err != nil {
		return err
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual string
	err = tx.QueryRowContext(ctx, "SELECT account_id FROM "+table+" WHERE account_id=? AND BINARY account_id=BINARY ? FOR UPDATE", id, id).Scan(&actual)
	// First login may create the account after admission. Management transactions
	// require an existing authoritative account row and cannot edit that case.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return tx.Commit()
}
