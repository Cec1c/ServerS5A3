package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentDungeonQuestStatusActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "accepted", "inprogress", "progress":
		return true
	}
	return false
}

type currentDungeonDeathQuestWork struct {
	huntEnemy, questDrops, huntMonster bool
}

func (s *Service) currentDungeonDeathQuestWorkFor(session *gameSession, runtime *runtimeDungeonState, monster runtimeDungeonMonster) currentDungeonDeathQuestWork {
	full := currentDungeonDeathQuestWork{true, true, true}
	repositories, ok := s.repositoryGroup()
	if !ok || session == nil || session.selectedCharacterID == 0 || runtime == nil || repositories.Character == nil || repositories.CharacterItems == nil {
		return full
	}
	reader, ok := repositories.Quest.(dnfrepo.CharacterQuestActivityReader)
	if !ok {
		if !s.currentDungeonDeathNeedsQuestWork(session) {
			return currentDungeonDeathQuestWork{}
		}
		return full
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	activities, found, err := reader.LoadCharacterQuestActivities(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !found {
		return full
	}
	var activeIDs []int64
	for _, activity := range activities {
		if currentDungeonQuestStatusActive(activity.Status) {
			activeIDs = append(activeIDs, activity.QuestID)
		}
	}
	if len(activeIDs) == 0 {
		return currentDungeonDeathQuestWork{}
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil || catalog == nil {
		return full
	}
	work := catalog.DungeonKillWork(activeIDs, dnfquest.HuntEnemyKillInput{
		DungeonID: runtime.Dungeon.ID, Difficulty: int64(runtime.Request.Difficulty),
		EnemyCode: monster.Spawn.MonsterID, EnemyType: currentDungeonQuestEnemyTypeForMonster(monster),
	})
	if work.Full {
		return full
	}
	// 普通怪任务物品原入口使用enemyType=1；直接复用同一匹配器和精确难度规则。
	drops := currentQuestDropMatchEnemy(catalog, activeIDs, runtime.Dungeon.ID, int64(runtime.Request.Difficulty), 1, monster.Spawn.MonsterID)
	return currentDungeonDeathQuestWork{work.HuntEnemy, len(drops) > 0, work.HuntMonster}
}

// 只在本次死亡的任务阶段使用。任何进行中任务（包括主任务的关联子任务）、
// 缺角色、错误或不支持窄查询时，均保留完整流程。下一只怪重新实时查询。
func (s *Service) currentDungeonDeathNeedsQuestWork(session *gameSession) bool {
	repositories, ok := s.repositoryGroup()
	if !ok || session == nil || session.selectedCharacterID == 0 || repositories.Character == nil || repositories.CharacterItems == nil {
		return true
	}
	reader, ok := repositories.Quest.(dnfrepo.CharacterQuestStatusReader)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	statuses, found, err := reader.LoadCharacterQuestStatuses(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !found {
		return true
	}
	for _, status := range statuses {
		if currentDungeonQuestStatusActive(status) {
			return true
		}
	}
	return false
}
