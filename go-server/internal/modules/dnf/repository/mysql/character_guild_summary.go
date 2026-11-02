package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

const characterGuildSummaryBatchSize = 256

// LoadCharacterGuildSummaries reads only the character-table columns required
// by guild and selection display. Calls are grouped by the actual read route and
// bounded to the MySQL placeholder budget.
func (s *mysqlCharStore) LoadCharacterGuildSummaries(ctx context.Context, ids []string) (map[string]repository.CharacterGuildSummary, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	unique := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := unique[id]; ok {
			continue
		}
		unique[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return map[string]repository.CharacterGuildSummary{}, nil
	}
	sort.Strings(normalized)
	// Compatibility off restores only the historical grow_type shadow override;
	// the default path remains the six-column character-table read.
	shadowGrowTypeOverrides := !characterMirrorStatsAuthoritative()

	groups := make(map[string][]string)
	tables := make([]string, 0)
	for _, id := range normalized {
		table, err := s.router.readTable(mysqlCharTable, id)
		if err != nil {
			return nil, err
		}
		if _, ok := groups[table]; !ok {
			tables = append(tables, table)
		}
		groups[table] = append(groups[table], id)
	}
	sort.Strings(tables)

	summaries := make(map[string]repository.CharacterGuildSummary, len(normalized))
	for _, table := range tables {
		groupIDs := groups[table]
		for start := 0; start < len(groupIDs); start += characterGuildSummaryBatchSize {
			end := start + characterGuildSummaryBatchSize
			if end > len(groupIDs) {
				end = len(groupIDs)
			}
			chunk := groupIDs[start:end]
			chunkSet := make(map[string]struct{}, len(chunk))
			for _, id := range chunk {
				chunkSet[id] = struct{}{}
			}
			if err := ctxErr(ctx); err != nil {
				return nil, err
			}
			query := ""
			args := stringsToAny(chunk)
			if shadowGrowTypeOverrides {
				// The shard is selected by the same character ID for both tables.
				statsTable, err := s.router.readTable(mysqlCharacterStatsTable, chunk[0])
				if err != nil {
					return nil, err
				}
				query = s.router.selectQuery(
					"SELECT c.character_id, c.account_id, c.name, c.job, c.level, COALESCE(s.stat_value, c.grow_type) AS grow_type FROM " +
						table + " AS c LEFT JOIN " + statsTable +
						" AS s ON s.character_id = c.character_id AND s.stat_key = ? WHERE c.character_id IN (" +
						placeholders(len(chunk)) + ") ORDER BY c.character_id ASC",
				)
				args = make([]any, 0, len(chunk)+1)
				args = append(args, repository.CharacterGrowTypeStatKey)
				args = append(args, stringsToAny(chunk)...)
			} else {
				query = s.router.selectQuery(
					"SELECT character_id, account_id, name, job, level, grow_type FROM " + table +
						" WHERE character_id IN (" + placeholders(len(chunk)) + ") ORDER BY character_id ASC",
				)
			}
			repository.RecordCharacterGuildSummaryQuery(ctx)
			rows, err := s.router.db.QueryContext(ctx, query, args...)
			if err != nil {
				// database/sql returns no usable Rows on query failure. The SQLRows
				// adapter may contain a typed nil, so do not call Close here.
				return nil, err
			}
			if rows == nil {
				return nil, errors.New("mysql: character guild summary query returned nil rows")
			}
			for rows.Next() {
				if err := ctxErr(ctx); err != nil {
					return nil, errors.Join(err, rows.Close())
				}
				var summary repository.CharacterGuildSummary
				var growType sql.NullInt64
				if err := rows.Scan(&summary.CharacterID, &summary.AccountID, &summary.Name, &summary.Job, &summary.Level, &growType); err != nil {
					return nil, errors.Join(err, rows.Close())
				}
				if growType.Valid {
					summary.GrowType = growType.Int64
				}
				if _, ok := chunkSet[summary.CharacterID]; !ok {
					continue
				}
				summaries[summary.CharacterID] = summary
			}
			if err := rows.Err(); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if err := ctxErr(ctx); err != nil {
				return nil, err
			}
		}
	}
	return summaries, nil
}
