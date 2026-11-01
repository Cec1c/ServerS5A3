package mysql

import (
	"context"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (u *mysqlAccountCharacterAssetUnitOfWork) WithinAccountCharacterSkillAssets(ctx context.Context, account, character string, apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.SkillRepository) error) error {
	if u == nil || strings.TrimSpace(account) == "" || strings.TrimSpace(character) == "" || apply == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	g := newMySQLGroupFromRouter(router, false)
	g.Character.(*mysqlCharStore).trackStatChanges = true
	g.Character.(*mysqlCharStore).activity = &mysqlCharacterActivityStore{mysqlActivityStore: g.Activity.(*mysqlActivityStore), characterID: character}
	g.AccountInventory.(*mysqlAccountInventoryStore).trackSlotChanges = true
	accountStore := &accountInventoryScopedRepository{accountID: account, base: g.AccountInventory}
	characterStore := &rentalScopedCharacterRepository{characterID: character, base: g.Character}
	skills := &characterProgressionScopedSkillRepository{characterID: character, base: g.Skill}
	if err := apply(accountStore, characterStore, skills); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
