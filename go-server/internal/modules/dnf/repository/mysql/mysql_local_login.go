package mysql

import (
	"context"
	"database/sql"
	"errors"

	"longheng.io/server/internal/modules/dnf/localaccount"
)

// Local credentials are in the existing fixed Windows table, not the game table
// prefix or a web-account table. Authentication uses the single primary DB;
// username cannot safely choose among account-ID shards or read replicas.
type mysqlLocalLoginStore struct {
	db    SQLDB
	table string
}

func localLoginStore(router mysqlRouter) localaccount.Repository {
	if len(router.writeDBs) != 1 {
		return nil
	}
	return &mysqlLocalLoginStore{db: router.db, table: quoteSQLIdentifier(router.writeDBs[0]) + ".`dnf_login_accounts`"}
}

func (s *mysqlLocalLoginStore) LoadCredential(ctx context.Context, username string) (localaccount.Credential, bool, error) {
	var c localaccount.Credential
	err := s.db.QueryRowContext(ctx, "SELECT account_id, username, password_hash, state FROM "+s.table+" WHERE username = ?", username).Scan(&c.AccountID, &c.Username, &c.PasswordHash, &c.State)
	if errors.Is(err, sql.ErrNoRows) {
		return localaccount.Credential{}, false, nil
	}
	if err != nil {
		return localaccount.Credential{}, false, localaccount.ErrUnavailable
	}
	return c, true, nil
}

func (s *mysqlLocalLoginStore) RecordLogin(ctx context.Context, c localaccount.Credential) error {
	result, err := s.db.ExecContext(ctx, "UPDATE "+s.table+" SET last_login_at = NOW(6), updated_at = NOW(6) WHERE account_id = ? AND username = ? AND password_hash = ? AND state = ?", c.AccountID, c.Username, c.PasswordHash, "active")
	if err != nil || result == nil {
		return localaccount.ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return localaccount.ErrUnavailable
	}
	return nil
}
