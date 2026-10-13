package gmtool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/localaccount"
)

const localLoginAccountTable = "dnf_login_accounts"

func (s *Server) usesLocalLoginAccounts() bool {
	return strings.TrimSpace(s.cfg.Table) == localLoginAccountTable
}

// Native accounts have canonical string keys, while this panel's existing API
// expects JavaScript-safe numeric selectors. This deterministic presentation ID
// never replaces a game account key or creates a database row. It is independent
// of query order, new registrations and server restarts. Every lookup verifies
// the full list for ambiguous selectors before returning an account owner.
func localAccountPanelID(accountID string) int64 {
	digest := sha256.Sum256([]byte("90cn-gm-local-account:" + accountID))
	id := int64(1 << 48)
	for i := range 6 {
		id |= int64(digest[i]) << (8 * (5 - i))
	}
	return id
}

func (s *Server) accountPanelID(accountID string) int {
	if s.usesLocalLoginAccounts() {
		accountID = strings.TrimSpace(accountID)
		if !localaccount.ValidAccountID(accountID) {
			return 0
		}
		return int(localAccountPanelID(accountID))
	}
	return panelIDFromAccountID(accountID)
}

func validateLocalAccountSelectors(accounts []webAccount) error {
	seen := make(map[int64]string, len(accounts))
	for _, account := range accounts {
		if _, exists := seen[account.PanelID]; exists {
			return errors.New("本地账号面板编号存在歧义，已拒绝账号读取")
		}
		seen[account.PanelID] = account.AccountID
	}
	return nil
}

func (s *Server) localLoginAccounts(ctx context.Context) ([]webAccount, error) {
	// The real local schema has no id/account/last_login_ip columns. Do not
	// create a legacy table or read/copy password hashes to emulate that schema.
	rows, err := s.db.QueryContext(ctx, "SELECT username, account_id, created_at, last_login_at FROM "+
		quoteIdentifier(s.cfg.Table)+" ORDER BY account_id")
	if err != nil {
		return nil, fmt.Errorf("读取本地账号列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	accounts := make([]webAccount, 0, 32)
	for rows.Next() {
		var account webAccount
		var createdAt, lastLoginAt sql.NullTime
		if err := rows.Scan(&account.Account, &account.AccountID, &createdAt, &lastLoginAt); err != nil {
			return nil, err
		}
		if !localaccount.ValidAccountID(account.AccountID) || strings.TrimSpace(account.Account) == "" {
			return nil, errors.New("本地账号记录无效，已拒绝账号读取")
		}
		account.PanelID = localAccountPanelID(account.AccountID)
		account.CreatedAt, account.LastLoginAt = nullTime(createdAt), nullTime(lastLoginAt)
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := validateLocalAccountSelectors(accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}
