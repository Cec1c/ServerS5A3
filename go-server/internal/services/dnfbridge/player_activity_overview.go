package dnfbridge

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
)

// 只投影既有账本；金币与排行榜使用同一组养成费用，不写重复总账。
func playerActivityOverviewMetrics(values map[string]int64) (map[string]int64, error) {
	out := completePlayerActivityMetrics(values)
	for _, metric := range []string{repo.ActivityRecommendedDungeon, repo.ActivityColorless} {
		n := values[metric]
		if n < 0 {
			return nil, repo.ErrActivityInvalid
		}
		out[metric] = n
	}
	var gold int64
	for _, metric := range repo.ActivityRankSourceMetrics(repo.ActivityGoldSpent) {
		n := values[metric]
		if n < 0 || n > math.MaxInt64-gold {
			return nil, repo.ErrActivityInvalid
		}
		out[metric] = n
		gold += n
	}
	out[repo.ActivityGoldSpent] = gold
	return out, nil
}
