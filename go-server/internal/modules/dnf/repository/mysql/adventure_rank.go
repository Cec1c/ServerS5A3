package mysql

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlCharStore) AdventureRankContributions(ctx context.Context) ([]repository.ActivityRankContribution, error) {
	if len(s.router.writeDBs) == 0 {
		return nil, repository.ErrActivityTransaction
	}
	parts := []string{}
	args := []any{}
	for _, database := range s.router.writeDBs {
		characters := mysqlTable(database, s.router.tablePrefix, mysqlCharTable)
		stats := mysqlTable(database, s.router.tablePrefix, mysqlCharacterStatsTable)
		parts = append(parts, "SELECT c.account_id, c.character_id, s.stat_value AS total FROM "+stats+" AS s INNER JOIN "+characters+" AS c ON c.character_id = s.character_id WHERE s.stat_key = ? AND s.stat_value > 0 AND c.delete_flag = 0")
		args = append(args, repository.ActivityRecommendedDungeon)
	}
	// 单条主库读取涵盖写分片，只扫描角色统计；15秒榜单缓存由领域层统一复用。
	query := "SELECT account_id, character_id, SUM(total) FROM (" + strings.Join(parts, " UNION ALL ") + ") AS adventure_counters GROUP BY account_id, character_id ORDER BY account_id, character_id LIMIT 100001"
	result, err := s.router.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	rows := []repository.ActivityRankContribution{}
	for result.Next() {
		var row repository.ActivityRankContribution
		if err := result.Scan(&row.AccountID, &row.CharacterID, &row.Score); err != nil {
			return nil, err
		}
		rows = append(rows, row)
		if len(rows) > repository.ActivityRankMaxContributions {
			return nil, repository.ErrActivityRank
		}
	}
	return rows, result.Err()
}
