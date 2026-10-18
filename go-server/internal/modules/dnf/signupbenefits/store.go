package signupbenefits

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

// Existing policy/signup table names and receipt keys stay unchanged.
// EnsureSchema never creates, deletes or modifies GM campaign/job tables.
type Store struct {
	db     *sql.DB
	prefix string
}

func NewStore(db *sql.DB, accountTable string) (*Store, error) {
	if db == nil || len(accountTable) > 40 || !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(accountTable) {
		return nil, errors.New("invalid benefits database/table")
	}
	return &Store{db: db, prefix: accountTable}, nil
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS " + s.prefix + "_benefit_policy (id INT PRIMARY KEY, revision BIGINT UNSIGNED NOT NULL, cera BIGINT NOT NULL, gold BIGINT NOT NULL) ENGINE=InnoDB",
		"INSERT IGNORE INTO " + s.prefix + "_benefit_policy (id,revision,cera,gold) VALUES (1,0,0,0)",
		"CREATE TABLE IF NOT EXISTS " + s.prefix + "_benefit_signup (account_id VARCHAR(64) PRIMARY KEY, cera BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Policy(ctx context.Context) (Policy, error) {
	var p Policy
	err := s.db.QueryRowContext(ctx, "SELECT revision,cera,gold FROM "+s.prefix+"_benefit_policy WHERE id=1").Scan(&p.Revision, &p.AccountCera, &p.CharacterGold)
	if err == nil {
		err = p.Validate()
	}
	return p, err
}

func (s *Store) SavePolicy(ctx context.Context, p Policy) (Policy, error) {
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	r, err := s.db.ExecContext(ctx, "UPDATE "+s.prefix+"_benefit_policy SET revision=revision+1,cera=?,gold=? WHERE id=1 AND revision=?", p.AccountCera, p.CharacterGold, p.Revision)
	if err != nil {
		return Policy{}, err
	}
	count, err := r.RowsAffected()
	if err != nil {
		return Policy{}, err
	}
	if count != 1 {
		return Policy{}, errors.New("设置已被其他窗口修改，请刷新后重试")
	}
	p.Revision++
	return p, nil
}

// CaptureSignup must run in the same SQL transaction as web registration.
// Old accounts have no entitlement. Later policy changes do not alter it.
func (s *Store) CaptureSignup(ctx context.Context, tx *sql.Tx, accountID string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO "+s.prefix+"_benefit_signup (account_id,cera) SELECT ?,cera FROM "+s.prefix+"_benefit_policy WHERE id=1", accountID)
	return err
}

func (s *Store) SignupCera(ctx context.Context, accountID string) (int64, error) {
	var amount int64
	err := s.db.QueryRowContext(ctx, "SELECT cera FROM "+s.prefix+"_benefit_signup WHERE account_id=?", accountID).Scan(&amount)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return amount, err
}
