package mysql

import (
	"context"
	"database/sql"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.CharacterExtraStatsReader = (*mysqlCharStore)(nil)

// LoadCharacterExtraStats reads a character's core fields and an explicit set
// of non-mirror stat keys from dnf_character_stats in a single LEFT JOIN query
// (one InnoDB snapshot, no ORDER BY). Missing characters return found=false;
// existing characters with no matching stat rows return found=true with empty
// Stats. Returned stat keys are matched case-sensitively in Go to preserve Go
// map semantics under utf8mb4_unicode_ci.
func (s *mysqlCharStore) LoadCharacterExtraStats(ctx context.Context, id string, statKeys ...string) (repository.CharacterMirrorStats, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctxErr(ctx); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	keys := repository.CharacterMirrorStatKeys(statKeys)
	if len(keys) == 0 {
		return s.LoadCharacterMirrorStats(ctx, id)
	}
	full := s.router.lockReads || s.trackStatChanges
	if !full {
		for _, key := range keys {
			if isMySQLCharacterMirrorStatKey(key) {
				full = true
				break
			}
		}
	}
	if full {
		record, found, err := s.Load(ctx, id)
		return repository.SelectCharacterMirrorStats(record, keys...), found, err
	}
	charTable, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	statsTable, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	var b strings.Builder
	b.Grow(len(charTable) + len(statsTable) + 192 + len(keys)*2)
	b.WriteString("SELECT c.character_id, c.account_id, c.slot, c.name, c.job, c.level, c.created_at, c.updated_at, s.stat_key, s.stat_value FROM ")
	b.WriteString(charTable)
	b.WriteString(" c LEFT JOIN ")
	b.WriteString(statsTable)
	b.WriteString(" s ON s.character_id = c.character_id AND s.stat_key IN (")
	for i := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	b.WriteString(") WHERE c.character_id = ?")
	args := make([]any, 0, len(keys)+1)
	for _, key := range keys {
		args = append(args, key)
	}
	args = append(args, id)
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(b.String()), args...)
	if err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	defer rows.Close()
	var record repository.CharacterMirrorStats
	var found bool
	var charID, accountID, name, job string
	var slot, level int
	var created, updated sql.NullTime
	var statKey sql.NullString
	var statValue sql.NullInt64
	dest := []any{&charID, &accountID, &slot, &name, &job, &level, &created, &updated, &statKey, &statValue}
	for rows.Next() {
		charID, accountID, name, job = "", "", "", ""
		slot, level = 0, 0
		created, updated = sql.NullTime{}, sql.NullTime{}
		statKey, statValue = sql.NullString{}, sql.NullInt64{}
		if err := rows.Scan(dest...); err != nil {
			return repository.CharacterMirrorStats{}, false, err
		}
		if !found {
			found = true
			record.CharacterID = charID
			record.AccountID = accountID
			record.Slot = slot
			record.Name = name
			record.Job = job
			record.Level = level
			record.CreatedAt = scanTime(created)
			record.UpdatedAt = scanTime(updated)
		}
		if statKey.Valid && statValue.Valid && containsExactStatKey(keys, statKey.String) {
			if record.Stats == nil {
				record.Stats = make(map[string]int64, len(keys))
			}
			record.Stats[statKey.String] = statValue.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	if err := rows.Close(); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	return record, found, nil
}

func containsExactStatKey(keys []string, candidate string) bool {
	for _, key := range keys {
		if key == candidate {
			return true
		}
	}
	return false
}
