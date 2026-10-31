package dnfbridge

import (
	"context"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 经验规划只消费独立技能点投影；实际提交仍走原事务并重新校验版本、归属。
// 不将窄投影拼成可整条保存的SkillRecord，避免触碰技能明细及双技能页。
func loadCurrentDungeonExperienceSkillPoints(ctx context.Context, skills dnfrepo.SkillRepository, characterID string) (dnfrepo.SkillPointLedger, bool, error) {
	if points, ok := skills.(dnfrepo.SkillPointLedgerRepository); ok {
		return points.LoadPointLedger(ctx, characterID)
	}
	full, found, err := skills.Load(ctx, characterID)
	if err != nil || !found {
		return dnfrepo.SkillPointLedger{}, found, err
	}
	return dnfrepo.SkillPointLedger{CharacterID: full.CharacterID, Points: full.Points, UpdatedAt: full.UpdatedAt}, true, nil
}
