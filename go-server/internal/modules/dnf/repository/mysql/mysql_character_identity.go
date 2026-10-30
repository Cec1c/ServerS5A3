package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlCharStore) LoadCharacterIdentity(ctx context.Context, id string) (repository.CharacterIdentity, bool, error) {
	table, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return repository.CharacterIdentity{}, false, err
	}
	var result repository.CharacterIdentity
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, account_id FROM "+table+" WHERE character_id = ?"), id).Scan(&result.CharacterID, &result.AccountID)
	if err != nil {
		found, scanErr := scanErr(err)
		return repository.CharacterIdentity{}, found, scanErr
	}
	return result, true, nil
}
