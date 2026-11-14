package mysql

import (
	"context"
	"database/sql"
	"errors"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlCharStore) LoadLevelUpRewardProbe(ctx context.Context, id string) (r.LevelUpRewardProbe, bool, error) {
	characters, err := s.router.readTable(mysqlCharTable, id)
	if err != nil {
		return r.LevelUpRewardProbe{}, false, err
	}
	stats, err := s.router.readTable(mysqlCharacterStatsTable, id)
	if err != nil {
		return r.LevelUpRewardProbe{}, false, err
	}
	// Both identity/level and candidate events come from the same SQL view.
	// The existing (character_id, stat_key) PK bounds this join to one character;
	// no schema change, cached absence, or display/roster loading is needed.
	query := "SELECT c.character_id, c.account_id, c.level, s.stat_key, s.stat_value FROM " + characters +
		" c LEFT JOIN " + stats + " s ON s.character_id=c.character_id AND s.stat_key LIKE ? ESCAPE '=' AND s.stat_value > 0 WHERE c.character_id = ?"
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), levelUpEventLikePrefix, id)
	if err != nil {
		return r.LevelUpRewardProbe{}, false, err
	}
	defer rows.Close()
	var probe r.LevelUpRewardProbe
	found := false
	for rows.Next() {
		var current r.LevelUpRewardProbe
		var key sql.NullString
		var value sql.NullInt64
		if err := rows.Scan(&current.CharacterID, &current.AccountID, &current.Level, &key, &value); err != nil {
			return r.LevelUpRewardProbe{}, false, err
		}
		if current.CharacterID != id || found && (current.AccountID != probe.AccountID || current.Level != probe.Level) {
			return r.LevelUpRewardProbe{}, false, errors.New("inconsistent level-up reward projection")
		}
		if !found {
			probe, found = current, true
		}
		if _, valid := r.LevelUpEventLevel(key.String); valid && key.Valid && value.Valid && value.Int64 > 0 {
			probe.Pending = true
		}
	}
	if err := rows.Err(); err != nil {
		return r.LevelUpRewardProbe{}, false, err
	}
	if err := rows.Close(); err != nil {
		return r.LevelUpRewardProbe{}, false, err
	}
	return probe, found, nil
}
