package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const guildRankingReadBatch = 128

func (s *mysqlCharStore) LoadGuildRankingSnapshot(ctx context.Context, accounts, members []string, limit int) (r.GuildRankingSnapshot, error) {
	beginner, ok := s.router.db.(sqlTxBeginner)
	if !ok || s.router.lockReads {
		return r.GuildRankingSnapshot{}, r.ErrGuildTransaction
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return r.GuildRankingSnapshot{}, err
	}
	defer tx.Rollback()
	router := s.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	result, err := readGuildRankingSnapshot(ctx, router, accounts, members, limit)
	if err != nil {
		return r.GuildRankingSnapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return r.GuildRankingSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return r.GuildRankingSnapshot{}, err
	}
	return result, nil
}

func readGuildRankingSnapshot(ctx context.Context, router mysqlRouter, accounts, members []string, limit int) (r.GuildRankingSnapshot, error) {
	result := r.GuildRankingSnapshot{Accounts: map[string][]r.GuildRankingCharacter{}, Members: map[string]r.GuildRankingCharacter{}}
	if limit < 1 || len(accounts) > 10000 || len(members) > 10000 || len(router.readDBs) == 0 {
		return result, r.ErrGuildTransaction
	}
	accounts = sortedUniqueStrings(accounts)
	members = sortedUniqueStrings(members)
	for _, id := range append(append([]string(nil), accounts...), members...) {
		if id == "" || strings.TrimSpace(id) != id {
			return result, r.ErrGuildTransaction
		}
	}
	for _, id := range accounts {
		result.Accounts[id] = nil
	}
	memberSet := map[string]bool{}
	for _, id := range members {
		memberSet[id] = true
	}
	seen := map[string]bool{}
	for _, database := range router.readDBs {
		table := mysqlTable(database, router.tablePrefix, mysqlCharTable)
		fields, joins := guildRankingColumns(router, database)
		for start := 0; start < len(accounts); start += guildRankingReadBatch {
			batch := accounts[start:min(start+guildRankingReadBatch, len(accounts))]
			query := "SELECT c.character_id, c.account_id, c.slot, c.level, " + fields + " FROM " + table + " c" + joins + " WHERE c.account_id IN (" + placeholders(len(batch)) + ") AND c.delete_flag = 0 ORDER BY c.slot, c.character_id"
			records, err := scanGuildRankingCharacters(ctx, router.db, query, stringsToAny(batch))
			if err != nil {
				return result, err
			}
			allowed := map[string]bool{}
			for _, id := range batch {
				allowed[id] = true
			}
			for _, c := range records {
				if c.CharacterID == "" || !allowed[c.AccountID] || seen[c.CharacterID] {
					return result, r.ErrGuildTransaction
				}
				seen[c.CharacterID] = true
				if len(result.Accounts[c.AccountID]) < limit {
					result.Accounts[c.AccountID] = append(result.Accounts[c.AccountID], c)
					if memberSet[c.CharacterID] {
						result.Members[c.CharacterID] = c
					}
				}
			}
		}
	}
	// Members absent from the selected account roster require the same owned
	// soft-deleted proof as the original sampler, including a concurrent deletion.
	missing := make([][]string, len(router.readDBs))
	for _, id := range members {
		if _, ok := result.Members[id]; !ok {
			shard := pickDatabase(id, len(missing))
			missing[shard] = append(missing[shard], id)
		}
	}
	for shard, ids := range missing {
		database := router.readDBs[shard]
		table := mysqlTable(database, router.tablePrefix, mysqlCharTable)
		fields, joins := guildRankingColumns(router, database)
		for start := 0; start < len(ids); start += guildRankingReadBatch {
			batch := ids[start:min(start+guildRankingReadBatch, len(ids))]
			records, err := scanGuildRankingCharacters(ctx, router.db, "SELECT c.character_id, c.account_id, c.slot, c.level, "+fields+" FROM "+table+" c"+joins+" WHERE c.character_id IN ("+placeholders(len(batch))+")", stringsToAny(batch))
			if err != nil {
				return result, err
			}
			allowed := map[string]bool{}
			for _, id := range batch {
				allowed[id] = true
			}
			for _, c := range records {
				if !allowed[c.CharacterID] {
					return result, r.ErrGuildTransaction
				}
				if _, dup := result.Members[c.CharacterID]; dup {
					return result, r.ErrGuildTransaction
				}
				result.Members[c.CharacterID] = c
			}
		}
	}
	// Only permanent raid clear facts of guild members are needed. Locations,
	// roster equipment, unrelated stats and characters' assets are never read.
	groups := make([][]string, len(router.readDBs))
	for id := range result.Members {
		shard := pickDatabase(id, len(groups))
		groups[shard] = append(groups[shard], id)
	}
	for shard, ids := range groups {
		sort.Strings(ids)
		table := mysqlTable(router.readDBs[shard], router.tablePrefix, mysqlCharacterStatsTable)
		for start := 0; start < len(ids); start += guildRankingReadBatch {
			batch := ids[start:min(start+guildRankingReadBatch, len(ids))]
			args := append(stringsToAny(batch), "raid_clear_v1_")
			query := "SELECT character_id, stat_key, stat_value FROM " + table + " WHERE character_id IN (" + placeholders(len(batch)) + ") AND LEFT(stat_key, 14) = ? ORDER BY character_id, stat_key"
			err := func() (err error) {
				rows, err := router.db.QueryContext(ctx, query, args...)
				if err != nil {
					return err
				}
				defer func() { err = errors.Join(err, rows.Close()) }()
				allowed := map[string]bool{}
				for _, id := range batch {
					allowed[id] = true
				}
				for rows.Next() {
					var id, key string
					var stamp int64
					if err = rows.Scan(&id, &key, &stamp); err != nil {
						return err
					}
					c, ok := result.Members[id]
					if !ok || !allowed[id] || !strings.HasPrefix(key, "raid_clear_v1_") {
						return r.ErrGuildTransaction
					}
					if c.RaidClears == nil {
						c.RaidClears = map[string]int64{}
					}
					if _, dup := c.RaidClears[key]; dup {
						return r.ErrGuildTransaction
					}
					c.RaidClears[key] = stamp
					result.Members[id] = c
				}
				return rows.Err()
			}()
			if err != nil {
				return result, err
			}
		}
	}
	// Overlay member facts into the account list without changing roster order.
	for account, characters := range result.Accounts {
		for i, c := range characters {
			if member, ok := result.Members[c.CharacterID]; ok {
				if member.AccountID != account {
					return result, r.ErrGuildTransaction
				}
				characters[i] = member
			}
		}
		result.Accounts[account] = characters
	}
	return result, nil
}

func guildRankingColumns(router mysqlRouter, database string) (string, string) {
	if characterMirrorStatsAuthoritative() {
		return "c.delete_flag, c.pvp_grade", ""
	}
	stats := mysqlTable(database, router.tablePrefix, mysqlCharacterStatsTable)
	return "COALESCE(d.stat_value, c.delete_flag), COALESCE(p.stat_value, c.pvp_grade)", " LEFT JOIN " + stats + " d ON d.character_id=c.character_id AND d.stat_key='delete_flag' LEFT JOIN " + stats + " p ON p.character_id=c.character_id AND p.stat_key='pvp_grade'"
}

func scanGuildRankingCharacters(ctx context.Context, db SQLDB, query string, args []any) (result []r.GuildRankingCharacter, err error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var c r.GuildRankingCharacter
		if err = rows.Scan(&c.CharacterID, &c.AccountID, &c.Slot, &c.Level, &c.DeleteFlag, &c.PVPGrade); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
