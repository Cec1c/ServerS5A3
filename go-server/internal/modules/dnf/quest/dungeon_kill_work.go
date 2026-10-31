package quest

// DungeonKillWork只判断本次目标是否可能触发原任务消费者，不规划或保存任务。
// 真正推进、重复事件、奖励及关联任务修复仍由原Owner负责。
type DungeonKillWork struct {
	HuntEnemy   bool
	HuntMonster bool
	// Full表示活动主任务可能激活子任务/同步父进度，或定义不可用。
	// 此时连任务物品也必须走原流程，避免漏掉刚激活的子任务。
	Full bool
}

func (c *Catalog) DungeonKillWork(activeIDs []int64, input HuntEnemyKillInput) DungeonKillWork {
	if c == nil {
		return DungeonKillWork{HuntEnemy: true, HuntMonster: true, Full: true}
	}
	var work DungeonKillWork
	for _, id := range activeIDs {
		definition, found := c.byID[id]
		if !found || questClearDefinition(definition) {
			return DungeonKillWork{HuntEnemy: true, HuntMonster: true, Full: true}
		}
		switch normalizeQuestTag(definition.Type) {
		case "hunt enemy":
			_, matches := huntEnemyMatchedChannel(definition.IntData, input)
			work.HuntEnemy = work.HuntEnemy || matches
		case "hunt monster":
			_, matches := huntMonsterMatchedChannel(definition.IntData, HuntMonsterKillInput{
				DungeonID: input.DungeonID, Difficulty: input.Difficulty, MonsterCode: input.EnemyCode,
			})
			work.HuntMonster = work.HuntMonster || matches
		}
	}
	return work
}
