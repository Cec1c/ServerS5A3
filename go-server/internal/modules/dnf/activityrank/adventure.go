package activityrank

import (
	"context"

	"longheng.io/server/internal/modules/dnf/repository"
)

// ReuseAdventureTotals让适级累计榜读取冒险团权威累计，不将历史次数写入当天账本。
// 日周榜沿用同次结算生成的日期汇总；其余榜单、周快照和奖励资格保持原仓储。
func ReuseAdventureTotals(store repository.ActivityRankingRepository, characters repository.CharacterRepository) repository.ActivityRankingRepository {
	return adventureTotalsStore{ActivityRankingRepository: store, characters: characters}
}

type adventureTotalsStore struct {
	repository.ActivityRankingRepository
	characters repository.CharacterRepository
}

func (s adventureTotalsStore) RankContributions(ctx context.Context, metric string, window repository.ActivityRankWindow) ([]repository.ActivityRankContribution, error) {
	if metric == repository.ActivityRecommendedDungeon && window.Period == "all" {
		if window.Validate() != nil {
			return nil, repository.ErrActivityRank
		}
		reader, ok := s.characters.(repository.AdventureRankingRepository)
		if !ok {
			return nil, repository.ErrActivityTransaction
		}
		return reader.AdventureRankContributions(ctx)
	}
	return s.ActivityRankingRepository.RankContributions(ctx, metric, window)
}
