package mysql

import (
	"context"
	"database/sql"
	"reflect"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func (u *mysqlGuildUnitOfWork) WithinGuildRead(ctx context.Context, apply func(r.GuildReadView) error) error {
	if u == nil || apply == nil {
		return r.ErrGuildTransaction
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return r.ErrGuildTransaction
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), router.writeDBs...)
	router.lockReads = false
	base := mysqlStoreBase{router: router}
	if err := apply(r.GuildReadView{Settings: r.ReadOnlyGuildSettings(&mysqlSettingsStore{mysqlStoreBase: base}), Characters: &mysqlGuildCharacters{router: router}}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

// Only the final compare/save takes the registry lock. Historical receipt
// scanning has already ended, and a concurrent writer makes this a no-op.
func (u *mysqlGuildUnitOfWork) CompareGuildRegistry(ctx context.Context, before, after r.SettingsRecord) (bool, error) {
	if u == nil || before.Scope != r.GuildRegistryScope || after.Scope != before.Scope {
		return false, r.ErrGuildTransaction
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return false, r.ErrGuildTransaction
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), router.writeDBs...)
	router.lockReads = true
	settings := &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: router}}
	actual, found, err := settings.Load(ctx, r.GuildRegistryScope)
	if err != nil || !found {
		return false, err
	}
	if !actual.UpdatedAt.Equal(before.UpdatedAt) || !reflect.DeepEqual(actual.Values, before.Values) {
		return false, nil
	}
	if err := settings.Save(ctx, after); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

type mysqlGuildCharacters struct{ router mysqlRouter }

func (s *mysqlGuildCharacters) LoadGuildCharacters(ctx context.Context, ids []string) (map[string]r.GuildCharacter, error) {
	if len(ids) > 1000 || len(s.router.writeDBs) == 0 {
		return nil, r.ErrGuildTransaction
	}
	groups := make([][]string, len(s.router.writeDBs))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, r.ErrGuildTransaction
		}
		if !seen[id] {
			groups[pickDatabase(id, len(groups))] = append(groups[pickDatabase(id, len(groups))], id)
			seen[id] = true
		}
	}
	result := make(map[string]r.GuildCharacter, len(seen))
	for shard, group := range groups {
		if len(group) == 0 {
			continue
		}
		database := s.router.writeDBs[shard]
		table := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		fields, joins := "c.grow_type, c.delete_flag", ""
		if !characterMirrorStatsAuthoritative() {
			stats := mysqlTable(database, s.router.tablePrefix, mysqlCharacterStatsTable)
			fields = "COALESCE(g.stat_value, c.grow_type), COALESCE(d.stat_value, c.delete_flag)"
			joins = " LEFT JOIN " + stats + " g ON g.character_id=c.character_id AND g.stat_key='grow_type' LEFT JOIN " + stats + " d ON d.character_id=c.character_id AND d.stat_key='delete_flag'"
		}
		query := "SELECT c.character_id, c.account_id, c.name, c.job, c.level, " + fields + " FROM " + table + " c" + joins + " WHERE c.character_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(group)), ",") + ")"
		args := make([]any, len(group))
		for i, id := range group {
			args[i] = id
		}
		rows, err := s.router.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var c r.GuildCharacter
				if err := rows.Scan(&c.CharacterID, &c.AccountID, &c.Name, &c.Job, &c.Level, &c.GrowType, &c.DeleteFlag); err != nil {
					return err
				}
				if !seen[c.CharacterID] || pickDatabase(c.CharacterID, len(groups)) != shard {
					return r.ErrGuildTransaction
				}
				if _, duplicate := result[c.CharacterID]; duplicate {
					return r.ErrGuildTransaction
				}
				result[c.CharacterID] = c
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
