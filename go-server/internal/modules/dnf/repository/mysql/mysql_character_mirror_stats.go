package mysql

import (
	"context"
	"database/sql"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var mysqlCharacterMirrorStatByKey = func() map[string]mysqlCharacterStatColumn {
	out := make(map[string]mysqlCharacterStatColumn, len(mysqlCharacterStatColumns))
	for _, column := range mysqlCharacterStatColumns {
		out[column.key] = column
	}
	return out
}()

var _ repository.CharacterMirrorStatsReader = (*mysqlCharStore)(nil)

func (s *mysqlCharStore) LoadCharacterMirrorStats(ctx context.Context, id string, statKeys ...string) (repository.CharacterMirrorStats, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctxErr(ctx); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	keys := repository.CharacterMirrorStatKeys(statKeys)
	full := s.router.lockReads || s.trackStatChanges || (len(keys) > 0 && !s.router.environment().mirrorStatsAuthoritative)
	for _, key := range keys {
		if _, ok := mysqlCharacterMirrorStatByKey[key]; !ok {
			full = true
			break
		}
	}
	if full {
		record, found, err := s.Load(ctx, id)
		return repository.SelectCharacterMirrorStats(record, keys...), found, err
	}
	table, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	query := s.router.characterMirrorQuery(table, keys)
	var record repository.CharacterMirrorStats
	var created, updated sql.NullTime
	values := make([]sql.NullInt64, len(keys))
	dest := make([]any, 0, 8+len(keys))
	dest = append(dest, &record.CharacterID, &record.AccountID, &record.Slot, &record.Name, &record.Job, &record.Level)
	for i := range values {
		dest = append(dest, &values[i])
	}
	dest = append(dest, &created, &updated)
	if err := s.router.db.QueryRowContext(ctx, query, id).Scan(dest...); err != nil {
		found, e := scanErr(err)
		return repository.CharacterMirrorStats{}, found, e
	}
	record.CreatedAt = scanTime(created)
	record.UpdatedAt = scanTime(updated)
	if len(keys) > 0 {
		record.Stats = make(map[string]int64, len(keys))
		for i, key := range keys {
			value := mysqlCharacterMirrorStatByKey[key].fallback
			if values[i].Valid {
				value = values[i].Int64
			}
			record.Stats[key] = value
		}
	}
	return record, true, nil
}

func (r mysqlRouter) characterMirrorQuery(table string, keys []string) string {
	if len(keys) == 0 {
		return r.characterMirrorBaseQuery(table)
	}
	cacheKey := table + "\x00" + keys[0]
	if len(keys) > 1 {
		cacheKey = table + "\x00" + strings.Join(keys, "\x00")
	}
	if r.sqlTemplates != nil {
		if q, ok := r.sqlTemplates.characterMirrorBase.Load(cacheKey); ok {
			return q.(string)
		}
	}
	var b strings.Builder
	b.WriteString("SELECT character_id, account_id, slot, name, job, level")
	for _, key := range keys {
		b.WriteString(", ")
		b.WriteString(quoteSQLIdentifier(mysqlCharacterMirrorStatByKey[key].column))
	}
	b.WriteString(", created_at, updated_at FROM ")
	b.WriteString(table)
	b.WriteString(" WHERE character_id = ?")
	q := b.String()
	if r.sqlTemplates == nil {
		return q
	}
	actual, _ := r.sqlTemplates.characterMirrorBase.LoadOrStore(cacheKey, q)
	return actual.(string)
}

func (r mysqlRouter) characterMirrorBaseQuery(table string) string {
	if r.sqlTemplates != nil {
		if q, ok := r.sqlTemplates.characterMirrorBase.Load(table); ok {
			return q.(string)
		}
	}
	q := "SELECT character_id, account_id, slot, name, job, level, created_at, updated_at FROM " + table + " WHERE character_id = ?"
	if r.sqlTemplates == nil {
		return q
	}
	actual, _ := r.sqlTemplates.characterMirrorBase.LoadOrStore(table, q)
	return actual.(string)
}
