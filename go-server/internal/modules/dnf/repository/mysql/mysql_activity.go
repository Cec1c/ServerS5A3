package mysql

import (
	"context"
	"errors"
	mysqldriver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

const activityEventsTable = "activity_events"
const activityCountersTable = "activity_counters"
const activityColumns = "event_id, account_id, character_id, character_name, metric, amount, occurred_at, dungeon_id, item_id, rarity, source"

type mysqlActivityStore struct{ mysqlStoreBase }

func (s *mysqlActivityStore) Record(ctx context.Context, e repository.ActivityEvent) (bool, error) {
	if !s.router.lockReads {
		return false, repository.ErrActivityTransaction
	}
	if err := e.Validate(); err != nil {
		return false, err
	}
	e.OccurredAt = e.OccurredAt.UTC().Truncate(1000)
	table, err := s.router.writeTable(activityEventsTable, e.AccountID)
	if err != nil {
		return false, err
	}
	_, err = s.router.db.ExecContext(ctx, "INSERT INTO "+table+" ("+activityColumns+", service_day) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", e.ID, e.AccountID, e.CharacterID, e.CharacterName, e.Metric, e.Amount, e.OccurredAt, e.DungeonID, e.ItemID, e.Rarity, e.Source, repository.ActivityDay(e.OccurredAt))
	if err != nil {
		var duplicate *mysqldriver.MySQLError
		if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
			return false, err
		}
		var old repository.ActivityEvent
		err = scanActivity(s.router.db.QueryRowContext(ctx, "SELECT "+activityColumns+" FROM "+table+" WHERE account_id = ? AND event_id = ? FOR UPDATE", e.AccountID, e.ID), &old)
		if err != nil {
			return false, err
		}
		if !repository.SameActivityEvent(old, e) {
			return false, repository.ErrActivityConflict
		}
		return false, nil
	}
	counters, err := s.router.writeTable(activityCountersTable, e.AccountID)
	if err != nil {
		return false, err
	}
	// 一次新增事件只写两条轻量汇总：角色累计、角色服务日；账号由这些持久行求和。
	_, err = s.router.db.ExecContext(ctx, "INSERT INTO "+counters+" (account_id, character_id, bucket, metric, total) VALUES (?, ?, 'all', ?, ?), (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE total = total + VALUES(total)", e.AccountID, e.CharacterID, e.Metric, e.Amount, e.AccountID, e.CharacterID, repository.ActivityDay(e.OccurredAt), e.Metric, e.Amount)
	if err == nil && e.Metric == repository.ActivityFatigue {
		stageBattlePassChange(s.router.db, s.router.battlePassChanges, repository.BattlePassChange{AccountID: e.AccountID, Fatigue: true})
	}
	return err == nil, err
}

func scanActivity(row interface{ Scan(...any) error }, e *repository.ActivityEvent) error {
	return row.Scan(&e.ID, &e.AccountID, &e.CharacterID, &e.CharacterName, &e.Metric, &e.Amount, &e.OccurredAt, &e.DungeonID, &e.ItemID, &e.Rarity, &e.Source)
}

func (s *mysqlActivityStore) Summary(ctx context.Context, q repository.ActivityQuery) (repository.ActivitySummary, error) {
	out := repository.NewActivitySummary(q.AccountID)
	if err := q.Validate(); err != nil {
		return out, err
	}
	table, err := s.router.readTable(activityCountersTable, q.AccountID)
	if err != nil {
		return out, err
	}
	where := "account_id = ?"
	args := []any{q.AccountID}
	if q.CharacterID != "" {
		where += " AND character_id = ?"
		args = append(args, q.CharacterID)
	}
	sourceWhere := where + " AND metric = ?"
	sourceArgs := append(append([]any{}, args...), repository.ActivityEpic)
	if q.FromDay == "" {
		where += " AND bucket = 'all'"
	} else {
		where += " AND bucket >= ? AND bucket <= ?"
		args = append(args, q.FromDay, q.ThroughDay)
	}
	query := "SELECT character_id, metric, SUM(total) FROM " + table + " WHERE " + where + " GROUP BY character_id, metric ORDER BY character_id, metric"
	if q.IncludeEpicSources {
		events, e := s.router.readTable(activityEventsTable, q.AccountID)
		if e != nil {
			return out, e
		}
		if q.FromDay != "" {
			sourceWhere += " AND service_day >= ? AND service_day <= ?"
			sourceArgs = append(sourceArgs, q.FromDay, q.ThroughDay)
		}
		// 一个SELECT读视图同时取总量和分项，避免拾取恰好提交在两次查询之间。
		query = "SELECT character_id, metric, SUM(total) AS amount, 0 AS dungeon_id, 0 AS epic_source FROM " + table + " WHERE " + where + " GROUP BY character_id, metric" +
			" UNION ALL SELECT character_id, metric, SUM(amount) AS amount, dungeon_id, 1 AS epic_source FROM " + events + " WHERE " + sourceWhere + " GROUP BY character_id, metric, dungeon_id" +
			" ORDER BY character_id, epic_source, dungeon_id, metric"
		args = append(args, sourceArgs...)
	}
	rows, err := s.router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var ch, m string
		var n int64
		var dungeon int64
		var source int
		if q.IncludeEpicSources {
			err = rows.Scan(&ch, &m, &n, &dungeon, &source)
		} else {
			err = rows.Scan(&ch, &m, &n)
		}
		if err != nil {
			return out, err
		}
		if source != 0 {
			if source != 1 || m != repository.ActivityEpic || dungeon <= 0 || n <= 0 {
				return out, repository.ErrActivityInvalid
			}
			out.EpicSources = append(out.EpicSources, repository.ActivityEpicSource{CharacterID: ch, DungeonID: dungeon, Amount: n})
			continue
		}
		if err = out.Add(ch, m, n); err != nil {
			return out, err
		}
	}
	out.Sort()
	return out, rows.Err()
}

func (s *mysqlActivityStore) Events(ctx context.Context, q repository.ActivityQuery) ([]repository.ActivityEvent, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	table, err := s.router.readTable(activityEventsTable, q.AccountID)
	if err != nil {
		return nil, err
	}
	where := "account_id = ?"
	args := []any{q.AccountID}
	if q.CharacterID != "" {
		where += " AND character_id = ?"
		args = append(args, q.CharacterID)
	}
	if q.FromDay != "" {
		where += " AND service_day >= ? AND service_day <= ?"
		args = append(args, q.FromDay, q.ThroughDay)
	}
	if !q.BeforeTime.IsZero() {
		where += " AND (occurred_at < ? OR (occurred_at = ? AND event_id < ?))"
		args = append(args, q.BeforeTime.UTC(), q.BeforeTime.UTC(), q.BeforeID)
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := s.router.db.QueryContext(ctx, "SELECT "+activityColumns+" FROM "+table+" WHERE "+where+" ORDER BY occurred_at DESC, event_id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []repository.ActivityEvent{}
	for rows.Next() {
		var e repository.ActivityEvent
		if err = scanActivity(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func activitySchema(database, prefix string) []string {
	return []string{
		"CREATE TABLE IF NOT EXISTS " + mysqlTable(database, prefix, activityEventsTable) + ` (
 account_id VARCHAR(128) NOT NULL,
 event_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 character_id VARCHAR(128) NOT NULL,
 character_name VARCHAR(64) NOT NULL,
 metric VARCHAR(64) NOT NULL,
 amount BIGINT NOT NULL,
 occurred_at DATETIME(6) NOT NULL,
 service_day CHAR(10) NOT NULL,
 dungeon_id BIGINT NOT NULL,
 item_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 rarity SMALLINT NOT NULL DEFAULT 0,
 source VARCHAR(32) NOT NULL,
 PRIMARY KEY (account_id,event_id),
 KEY idx_activity_account_time (account_id,occurred_at,event_id),
 KEY idx_activity_character_time (account_id,character_id,occurred_at,event_id),
 KEY idx_activity_day (account_id,service_day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		"CREATE TABLE IF NOT EXISTS " + mysqlTable(database, prefix, activityCountersTable) + ` (
 account_id VARCHAR(128) NOT NULL,
 character_id VARCHAR(128) NOT NULL,
 bucket VARCHAR(10) NOT NULL,
 metric VARCHAR(64) NOT NULL,
 total BIGINT NOT NULL,
 PRIMARY KEY (account_id,bucket,character_id,metric)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
}

// 所有权查询与插入使用同一事务；角色按角色键路由，账本按账号键路由。
type mysqlCharacterActivityStore struct {
	*mysqlActivityStore
	characterID string
}

func (s *mysqlCharacterActivityStore) Record(ctx context.Context, e repository.ActivityEvent) (bool, error) {
	if e.CharacterID != s.characterID {
		return false, repository.ErrActivityTransaction
	}
	table, err := s.router.writeTable(mysqlCharTable, s.characterID)
	if err != nil {
		return false, err
	}
	var account, name string
	err = s.router.db.QueryRowContext(ctx, "SELECT account_id, name FROM "+table+" WHERE character_id = ? FOR UPDATE", s.characterID).Scan(&account, &name)
	if err != nil {
		return false, err
	}
	if account == "" || account != e.AccountID {
		return false, repository.ErrActivityTransaction
	}
	e.CharacterName = name
	return s.mysqlActivityStore.Record(ctx, e)
}
