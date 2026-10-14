package gmtool

import (
	"context"
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/repository"
)

// ScanNewCharacters is called by the independent GM worker. Cursor is merely
// progress, not delivery proof: permanent transactional receipts are authority.
// A restart can safely rescan; each completed pass revisits existing accounts
// because a new character need not belong to a newly registered account.
func (s *Server) ScanNewCharacters(ctx context.Context, cursor int64, limit int) (int64, error) {
	if cursor < 0 || limit < 1 || limit > 100 {
		return cursor, errors.New("invalid benefit scan range")
	}
	if s.deps.Benefits == nil {
		return cursor, errors.New("benefit store unavailable")
	}
	campaigns, err := s.deps.Benefits.Campaigns(ctx)
	if err != nil {
		return cursor, err
	}
	active := false
	for _, campaign := range campaigns {
		active = active || (campaign.Enabled && campaign.AutoNewCharacters)
	}
	if !active {
		return 0, nil
	}
	group, err := s.repositories()
	if err != nil {
		return cursor, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,account_id FROM "+quoteIdentifier(s.cfg.Table)+" WHERE id>? ORDER BY id LIMIT ?", cursor, limit)
	if err != nil {
		return cursor, err
	}
	type account struct {
		id  int64
		key string
	}
	var accounts []account
	for rows.Next() {
		var a account
		if err = rows.Scan(&a.id, &a.key); err != nil {
			rows.Close()
			return cursor, err
		}
		accounts = append(accounts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return cursor, err
	}
	var failures []error
	for _, a := range accounts {
		if err = ctx.Err(); err != nil {
			return cursor, errors.Join(append(failures, err)...)
		}
		characters, loadErr := group.Character.ListByAccount(ctx, a.key, repository.DefaultCharacterSlotLimit+1)
		if loadErr == nil && len(characters) > repository.DefaultCharacterSlotLimit {
			loadErr = errors.New("account character limit exceeded")
		}
		if loadErr != nil {
			failures = append(failures, fmt.Errorf("benefit scan account %d: %w", a.id, loadErr))
		} else {
			for _, c := range characters {
				if c.AccountID != a.key {
					failures = append(failures, errors.New("benefit scan ownership mismatch"))
					continue
				}
				if err = s.deps.Benefits.DeliverNewCharacter(ctx, group, c); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if ctx.Err() != nil {
			return cursor, errors.Join(append(failures, ctx.Err())...)
		}
		// Failed accounts remain eligible and are retried on the next full pass.
		cursor = a.id
	}
	if len(accounts) < limit {
		cursor = 0
	}
	return cursor, errors.Join(failures...)
}
