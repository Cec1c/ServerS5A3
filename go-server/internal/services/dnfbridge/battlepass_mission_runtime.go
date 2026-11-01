package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// 普通、深渊等有真实最终房间结算的地下城；独立塔、锦标赛及团本不混入普通通关任务。
func currentBattlePassDungeonClear(r *runtimeDungeonState) bool {
	return r != nil && r.ordinaryFinalRoomClearAccepted && !r.tutorialFinalRoomClearAccepted && r.raidEntry == nil && r.Tournament == nil && !currentDungeonUsesDedicatedTowerResult(r) && !r.IgnoreDefaultDungeonClear
}

func currentBattlePassBossMonster(monster runtimeDungeonMonster) bool {
	kind, err := currentDungeonMonsterActorType(monster.Spawn)
	return err == nil && kind == 3
}

// 版本前置条件与经验共用原子提交；满级及零经验怪也保留受保护增量。
func addBattlePassKillPlan(plan *progression.CommitPlan, ch repo.CharacterRecord, award currentDungeonMonsterExperienceAward, now time.Time) {
	if !award.BattlePassMonster {
		return
	}
	next := repo.CloneCharacter(ch)
	repo.AddBattlePassCounter(&next, 1, -1, 1, now)
	if award.BattlePassBoss {
		repo.AddBattlePassCounter(&next, 2, -1, 1, now)
	}
	if plan.ExpectedCharacterStats == nil {
		plan.ExpectedCharacterStats = map[string]int64{}
	}
	if plan.NextCharacterStats == nil {
		plan.NextCharacterStats = map[string]int64{}
	}
	for key, value := range next.Stats {
		if value != ch.Stats[key] {
			plan.ExpectedCharacterStats[key] = ch.Stats[key]
			plan.NextCharacterStats[key] = value
		}
	}
}
