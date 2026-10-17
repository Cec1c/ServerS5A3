package progression

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// ApplyCharacterExperience 是真实经验提交的统一入口。调用方必须传入事务内加载
// 的记录，并在同一事务保存角色和技能；这里只应用已经核准的计划，不另开事务。
// 建角、GM改级、同级经验变化不会产生可追溯不到真实升级的历史补发。
func ApplyCharacterExperience(character *dnfrepo.CharacterRecord, skill *dnfrepo.SkillRecord, nextLevel int, nextExperience uint32, nextPoints dnfrepo.SkillPointState, now time.Time) error {
	if character == nil || skill == nil || character.Level < 1 || nextLevel < character.Level || nextLevel > 255 || nextPoints.SyncedLevel != nextLevel {
		return ErrCommitPlanInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stamp := now.Unix()
	if stamp <= 0 {
		return ErrCommitPlanInvalid
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	for level := character.Level + 1; level <= nextLevel; level++ {
		key := dnfrepo.LevelUpEventKey(level)
		if character.Stats[key] == 0 {
			character.Stats[key] = stamp
		}
	}
	character.Level = nextLevel
	character.Stats[characterExperienceStatKey] = int64(nextExperience)
	character.UpdatedAt = now.UTC()
	skill.Points = nextPoints
	skill.UpdatedAt = now.UTC()
	return nil
}
