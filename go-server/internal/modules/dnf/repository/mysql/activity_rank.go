package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

const activityRankWeeksTable = "activity_rank_weeks"
const activityRankRewardsTable = "activity_rank_rewards"
const activityRankSnapshotMaxBytes = 32 << 20

func (s *mysqlActivityStore) RankContributions(ctx context.Context, metric string, w repository.ActivityRankWindow) ([]repository.ActivityRankContribution, error) {
	if !repository.ValidActivityRankMetric(metric) || w.Validate() != nil || len(s.router.writeDBs) == 0 {
		return nil, repository.ErrActivityRank
	}
	// 同一物理连接的一条SELECT覆盖写分片，避免副本滞后或分片间读视图不同。
	parts := []string{}
	args := []any{}
	for _, database := range s.router.writeDBs {
		q := "SELECT account_id, character_id, total FROM " + mysqlTable(database, s.router.tablePrefix, activityCountersTable)
		sources := repository.ActivityRankSourceMetrics(metric)
		if len(sources) == 1 {
			q += " WHERE metric = ?"
		} else {
			q += " WHERE metric IN (" + strings.TrimSuffix(strings.Repeat("?,", len(sources)), ",") + ")"
		}
		for _, source := range sources {
			args = append(args, source)
		}
		if w.Period == "all" {
			q += " AND bucket = 'all'"
		} else {
			q += " AND bucket >= ? AND bucket <= ?"
			args = append(args, w.FromDay, w.ThroughDay)
		}
		parts = append(parts, q)
	}
	query := "SELECT account_id, character_id, SUM(total) FROM (" + strings.Join(parts, " UNION ALL ") + ") AS rank_counters GROUP BY account_id, character_id HAVING SUM(total) > 0 ORDER BY account_id, character_id LIMIT 100001"
	rows, err := s.router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []repository.ActivityRankContribution{}
	for rows.Next() {
		var r repository.ActivityRankContribution
		if err := rows.Scan(&r.AccountID, &r.CharacterID, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
		if len(out) > repository.ActivityRankMaxContributions {
			return nil, repository.ErrActivityRank
		}
	}
	return out, rows.Err()
}

func (s *mysqlActivityStore) RankWeek(ctx context.Context, metric, week string) (repository.ActivityRankSnapshot, bool, error) {
	if _, err := repository.ActivityRankWeek(week); err != nil || !repository.ValidActivityRankMetric(metric) {
		return repository.ActivityRankSnapshot{}, false, repository.ErrActivityRank
	}
	table, err := s.router.writeTable(activityRankWeeksTable, metric+":"+week)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	var payload []byte
	err = s.router.db.QueryRowContext(ctx, "SELECT snapshot_json FROM "+table+" WHERE board_key = ?", metric+":"+week).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ActivityRankSnapshot{}, false, nil
	}
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	v, err := decodeActivityRankSnapshot(payload, metric, week)
	return v, err == nil, err
}
func decodeActivityRankSnapshot(b []byte, metric, week string) (repository.ActivityRankSnapshot, error) {
	var v repository.ActivityRankSnapshot
	if len(b) == 0 || len(b) > activityRankSnapshotMaxBytes {
		return v, repository.ErrActivityRank
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	if v.Metric != metric || v.Window.FromDay != week || v.Window.Validate() != nil || v.Window.Period != "week" || !v.Window.Closed(v.SettledAt) {
		return v, repository.ErrActivityRank
	}
	digest, err := v.Policy.Digest()
	if err != nil || digest != v.PolicyDigest {
		return v, repository.ErrActivityRank
	}
	return v, nil
}

func (s *mysqlActivityStore) SettleRankWeek(ctx context.Context, req repository.ActivityRankSettlement) (repository.ActivityRankSnapshot, bool, error) {
	if err := req.Validate(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	beginner, ok := s.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ActivityRankSnapshot{}, false, repository.ErrActivityTransaction
	}
	table, err := s.router.writeTable(activityRankWeeksTable, req.Key())
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	tx, err := beginner.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	defer tx.Rollback()
	// 同周同指标先锁定唯一快照行；未写完时任何失败连快照、奖励资格一起回滚。
	_, err = tx.ExecContext(ctx, "INSERT INTO "+table+" (board_key, snapshot_json) VALUES (?, '') ON DUPLICATE KEY UPDATE board_key = board_key", req.Key())
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	var old []byte
	if err = tx.QueryRowContext(ctx, "SELECT snapshot_json FROM "+table+" WHERE board_key = ? FOR UPDATE", req.Key()).Scan(&old); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if len(old) > 0 {
		v, e := decodeActivityRankSnapshot(old, req.Metric, req.Week)
		if e != nil {
			return v, false, e
		}
		digest, _ := req.Policy.Digest()
		if digest != v.PolicyDigest {
			return repository.ActivityRankSnapshot{}, false, repository.ErrActivityRankConflict
		}
		return v, false, tx.Commit()
	}
	router := s.router
	router.db = sqlTransactionDB{tx: tx}
	store := &mysqlActivityStore{mysqlStoreBase{router: router}}
	w, _ := repository.ActivityRankWeek(req.Week)
	rows, err := store.RankContributions(ctx, req.Metric, w)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	snapshot, err := repository.NewActivityRankSnapshot(req, rows)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if len(payload) > activityRankSnapshotMaxBytes {
		return repository.ActivityRankSnapshot{}, false, repository.ErrActivityRank
	}
	for _, r := range snapshot.Rewards {
		rewardTable, e := router.writeTable(activityRankRewardsTable, r.AccountID)
		if e != nil {
			return repository.ActivityRankSnapshot{}, false, e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO "+rewardTable+" (account_id, board_key, rank_no, score, appearance_code, state) VALUES (?, ?, ?, ?, ?, 'pending')", r.AccountID, r.BoardKey, r.Rank, r.Score, r.AppearanceCode)
		if e != nil {
			return repository.ActivityRankSnapshot{}, false, e
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET snapshot_json = ? WHERE board_key = ?", payload, req.Key()); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return repository.ActivityRankSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s *mysqlActivityStore) PendingRankRewards(ctx context.Context, account string, limit int) ([]repository.ActivityRankReward, error) {
	if strings.TrimSpace(account) == "" || len(account) > 128 || limit < 1 || limit > 100 {
		return nil, repository.ErrActivityRank
	}
	table, err := s.router.writeTable(activityRankRewardsTable, account)
	if err != nil {
		return nil, err
	}
	rows, err := s.router.db.QueryContext(ctx, "SELECT board_key, account_id, rank_no, score, appearance_code, state FROM "+table+" WHERE account_id = ? AND state = 'pending' ORDER BY board_key LIMIT ?", account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []repository.ActivityRankReward{}
	for rows.Next() {
		var r repository.ActivityRankReward
		if err = rows.Scan(&r.BoardKey, &r.AccountID, &r.Rank, &r.Score, &r.AppearanceCode, &r.State); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func activityRankSchema(database, prefix string) []string {
	return []string{
		"CREATE TABLE IF NOT EXISTS " + mysqlTable(database, prefix, activityRankWeeksTable) + ` (
 board_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 snapshot_json LONGTEXT NOT NULL,
 PRIMARY KEY (board_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		"CREATE TABLE IF NOT EXISTS " + mysqlTable(database, prefix, activityRankRewardsTable) + ` (
 account_id VARCHAR(128) NOT NULL,
 board_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 rank_no INT NOT NULL,
 score BIGINT NOT NULL,
 appearance_code VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY (account_id,board_key),
 KEY idx_rank_pending (account_id,state,board_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
}
