package mysql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

func clearHistorySchema(database, prefix string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
 character_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 completion_hash BINARY(32) NOT NULL,
 account_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 character_name VARCHAR(128) NOT NULL,
 dungeon_id BIGINT NOT NULL,
 dungeon_name VARCHAR(255) NOT NULL,
 difficulty INT NOT NULL,
 started_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NOT NULL,
 duration_ms BIGINT NOT NULL,
 fatigue_spent BIGINT NULL,
 PRIMARY KEY(character_id,completion_hash),
 KEY clear_time(finished_at), KEY clear_dungeon(dungeon_id,finished_at),
 KEY clear_account(account_id,finished_at)
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, mysqlTable(database, prefix, "clear_history"))
}

func (s *mysqlCharStore) AppendClear(ctx context.Context, c moderation.Clear) error {
	if c.Key == "" || c.CharacterID == "" || c.AccountID == "" || c.DungeonID <= 0 || c.FinishedAt.IsZero() || c.DurationMS < 0 || (c.FatigueSpent != nil && *c.FatigueSpent < 0) {
		return repository.ErrCharacterSettlementTransactionUnavailable
	}
	table, err := s.router.writeTable("clear_history", c.CharacterID)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(c.Key))
	_, err = s.router.db.ExecContext(ctx, "INSERT INTO "+table+" (character_id,completion_hash,account_id,character_name,dungeon_id,dungeon_name,difficulty,started_at,finished_at,duration_ms,fatigue_spent) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE completion_hash=VALUES(completion_hash)", c.CharacterID, hash[:], c.AccountID, c.CharacterName, c.DungeonID, c.DungeonName, c.Difficulty, c.StartedAt, c.FinishedAt, c.DurationMS, c.FatigueSpent)
	return err
}
func (r *characterSettlementScopedCharacterRepository) AppendClear(ctx context.Context, c moderation.Clear) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, c.CharacterID); err != nil {
		return err
	}
	w, ok := r.base.(repository.ClearHistoryWriter)
	if !ok {
		return repository.ErrCharacterSettlementTransactionUnavailable
	}
	return w.AppendClear(ctx, c)
}

func (s *mysqlCharStore) QueryClears(ctx context.Context, q moderation.Query) (moderation.ClearPage, error) {
	out := moderation.ClearPage{Rows: []moderation.ClearStats{}, From: q.From, To: q.To}
	if err := q.Normalize(time.Now()); err != nil {
		return out, err
	}
	if err := q.NormalizeSort("clears"); err != nil {
		return out, err
	}
	out.From, out.To = q.From, q.To
	var parts []string
	var args []any
	// Read the authoritative write shards; each character is routed to exactly one.
	for _, database := range s.router.writeDBs {
		where := "finished_at>=? AND finished_at<?"
		values := []any{q.From, q.To}
		if q.DungeonID != 0 {
			where += " AND dungeon_id=?"
			values = append(values, q.DungeonID)
		}
		if q.Search != "" {
			where += " AND (character_id=? OR account_id=? OR LOCATE(?,character_name)>0 OR LOCATE(?,dungeon_name)>0)"
			values = append(values, q.Search, q.Search, q.Search, q.Search)
		}
		parts = append(parts, "SELECT account_id,character_id,character_name,dungeon_id,dungeon_name,difficulty,finished_at,duration_ms,fatigue_spent FROM "+mysqlTable(database, s.router.tablePrefix, "clear_history")+" WHERE "+where)
		args = append(args, values...)
	}
	if len(parts) == 0 {
		return out, repository.ErrCharacterSettlementTransactionUnavailable
	}
	source := "SELECT *,COUNT(*) OVER(PARTITION BY character_id,dungeon_id,difficulty,DATE_FORMAT(finished_at,'%Y-%m-%d %H')) AS hourly FROM (" + strings.Join(parts, " UNION ALL ") + ") source_clears"
	columns := map[string]string{
		"accountId": "account_id", "characterId": "character_id", "characterName": "MAX(character_name)",
		"dungeonId": "dungeon_id", "dungeonName": "MAX(dungeon_name)", "difficulty": "difficulty",
		"count": "COUNT(*)", "fastestMs": "MIN(NULLIF(duration_ms,0))", "averageMs": "AVG(NULLIF(duration_ms,0))",
		"peakHourly": "MAX(hourly)", "lastClearAt": "MAX(finished_at)", "fatigueSpent": "SUM(fatigue_spent)", "averageFatigue": "AVG(fatigue_spent)",
	}
	column := columns[q.SortBy]
	order := "(" + column + " IS NULL) ASC," + column + " " + q.SortOrder + ",character_id,dungeon_id,difficulty,account_id"
	query := "SELECT account_id,character_id,MAX(character_name),dungeon_id,MAX(dungeon_name),difficulty,COUNT(*),MAX(finished_at),COALESCE(MIN(NULLIF(duration_ms,0)),0),COALESCE(CAST(AVG(NULLIF(duration_ms,0)) AS SIGNED),0),MAX(hourly),SUM(fatigue_spent),AVG(fatigue_spent),COUNT(fatigue_spent) FROM (" + source + ") clears GROUP BY account_id,character_id,dungeon_id,difficulty HAVING (?=0 OR MIN(NULLIF(duration_ms,0))<=?) AND (?=0 OR MAX(hourly)>=?) ORDER BY " + order + " LIMIT ? OFFSET ?"
	args = append(args, q.MaxDurationMS, q.MaxDurationMS, q.MinHourlyPeak, q.MinHourlyPeak, q.Limit+1, q.Offset)
	rows, err := s.router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c moderation.ClearStats
		if err := rows.Scan(&c.AccountID, &c.CharacterID, &c.CharacterName, &c.DungeonID, &c.DungeonName, &c.Difficulty, &c.Count, &c.LastClearAt, &c.FastestMS, &c.AverageMS, &c.PeakHourly, &c.FatigueSpent, &c.AverageFatigue, &c.FatigueSamples); err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, c)
	}
	if len(out.Rows) > q.Limit {
		out.HasMore = true
		out.Rows = out.Rows[:q.Limit]
	}
	return out, rows.Err()
}
