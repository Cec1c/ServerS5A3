package repository

import "context"

// 与冒险团适级通关的角色持久键共用，累计榜不另建或回填一份累计数。
const ActivityRecommendedDungeon = "adventure_recommended_dungeon_clears"

// AdventureRankingRepository只读取未删除角色的适级累计，不加载背包或全服事件。
type AdventureRankingRepository interface {
	AdventureRankContributions(context.Context) ([]ActivityRankContribution, error)
}
