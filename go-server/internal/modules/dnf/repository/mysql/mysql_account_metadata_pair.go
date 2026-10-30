package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strings"
	"time"
)

func (s *mysqlAccountStore) UpdateMetadataPair(ctx context.Context, left, right string, apply func(repository.AccountRecord, repository.AccountRecord) (map[string]string, map[string]string, error), at time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := s.router.db.(sqlTxBeginner)
	if !ok || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" || left == right || apply == nil || len(s.router.writeDBs) == 0 {
		return repository.ErrAccountMetadataUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	router := s.router
	router.db = newSQLTransactionDB(tx, router.db)
	router.readDBs = append([]string(nil), router.writeDBs...)
	router.lockReads = true
	store := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: router}}
	ids := []string{left, right}
	sort.Strings(ids)
	owners := make(map[string]repository.AccountRecord, 2)
	// Deterministic owner order prevents opposite-direction invitations from
	// locking A/B and B/A. Metadata reads use the same transaction and writer DB.
	for _, id := range ids {
		row, found, loadErr := store.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return repository.ErrAccountMetadataUnavailable
		}
		owners[id] = row
	}
	ac, bc, err := apply(repository.CloneAccount(owners[left]), repository.CloneAccount(owners[right]))
	if err != nil {
		return err
	}
	if !repository.ValidAccountMetadataChanges(ac) || !repository.ValidAccountMetadataChanges(bc) {
		return repository.ErrAccountMetadataKeyRequired
	}
	changes := map[string]map[string]string{left: ac, right: bc}
	at = timeOrNow(at, router.now)
	for _, id := range ids {
		entries := changes[id]
		if len(entries) == 0 {
			continue
		}
		table, tableErr := router.writeTable(mysqlAccountTable, id)
		if tableErr != nil {
			return tableErr
		}
		metadataTable, tableErr := router.writeTable(mysqlAccountMetadataTable, id)
		if tableErr != nil {
			return tableErr
		}
		if _, err = router.db.ExecContext(ctx, "UPDATE "+table+" SET updated_at = ? WHERE account_id = ?", at, id); err != nil {
			return err
		}
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, err = router.db.ExecContext(ctx, buildUpsert(metadataTable, []string{"account_id", "entry_key", "entry_value"}, []string{updateValue("entry_value")}), id, key, entries[key])
			if err != nil {
				return err
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	committed = true
	return nil
}
