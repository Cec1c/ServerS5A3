package mysql

import (
	"context"
	"database/sql"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlSettingsStore) WalkGuildSettlementReceipts(ctx context.Context, guildID uint32, since time.Time, visit func(repository.SettingsRecord) error) error {
	if visit == nil || guildID == 0 || len(s.router.writeDBs) == 0 {
		return repository.ErrGuildTransaction
	}
	for shard, database := range s.router.writeDBs {
		settings := mysqlTable(database, s.router.tablePrefix, mysqlSettingsTable)
		values := mysqlTable(database, s.router.tablePrefix, mysqlSettingValuesTable)
		// No FOR UPDATE: the caller holds the guild registry lock and receipts
		// are immutable. The prefix limits the scan to dungeon settlements.
		query := "SELECT s.scope, s.updated_at, v.entry_value FROM " + settings + " s JOIN " + values + " v ON v.scope = s.scope AND v.entry_key = ? WHERE s.scope LIKE ? AND CASE WHEN JSON_VALID(v.entry_value) THEN JSON_EXTRACT(v.entry_value, '$.GuildDungeon.GuildID') ELSE ? END = ?"
		args := []any{"receipt", "guild-clear:v1:%", guildID, guildID}
		if !since.IsZero() {
			query += " AND s.updated_at >= ?"
			args = append(args, since.UTC())
		}
		rows, err := s.router.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var record repository.SettingsRecord
				var at sql.NullTime
				var receipt string
				if err := rows.Scan(&record.Scope, &at, &receipt); err != nil {
					return err
				}
				if pickDatabase(record.Scope, len(s.router.writeDBs)) != shard {
					return repository.ErrGuildTransaction
				}
				record.UpdatedAt = scanTime(at)
				record.Values = map[string]string{"receipt": receipt}
				if err := visit(record); err != nil {
					return err
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
