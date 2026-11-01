package memory

import (
	"context"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *memoryCharStore) AdventureRankContributions(ctx context.Context) ([]repository.ActivityRankContribution, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	rows := []repository.ActivityRankContribution{}
	for _, ch := range s.Snapshot(func(a, b repository.CharacterRecord) bool { return a.CharacterID < b.CharacterID }) {
		if ch.Stats["delete_flag"] != 0 || ch.Stats[repository.ActivityRecommendedDungeon] <= 0 {
			continue
		}
		rows = append(rows, repository.ActivityRankContribution{AccountID: ch.AccountID, CharacterID: ch.CharacterID, Score: ch.Stats[repository.ActivityRecommendedDungeon]})
		if len(rows) > repository.ActivityRankMaxContributions {
			return nil, repository.ErrActivityRank
		}
	}
	return rows, ctx.Err()
}
