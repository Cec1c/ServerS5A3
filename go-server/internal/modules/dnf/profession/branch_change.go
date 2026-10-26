package profession

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/skill"
)

// PlanBranchChange 仅用于已转职角色的转职变更，不放宽任务转职和觉醒的前置。
// 分支和保留的觉醒阶段必须真实存在于当前PVF；技能基线也必须可重建。
func (p *Profiles) PlanBranchChange(catalog *skill.Table, job, current, target byte, preserveAwakening bool) (Transition, error) {
	first, stage := Decode(current)
	if first == 0 || first == target || target > 15 || stage > 2 {
		return Transition{}, ErrTransitionInvalid
	}
	if _, err := p.ResetGrants(catalog, job, current, 1); err != nil {
		return Transition{}, err
	}
	if !preserveAwakening {
		if stage != 0 {
			return Transition{}, ErrTransitionOutOfStep
		}
		stage = 0
	}
	packed, err := Encode(target, stage)
	if err != nil {
		return Transition{}, err
	}
	if _, err := p.ResetGrants(catalog, job, packed, 1); err != nil {
		return Transition{}, fmt.Errorf("%w: job=%d target=0x%02x: %w", ErrTransitionInvalid, job, packed, err)
	}
	return Transition{Kind: KindClassChange, ChainType: 1, GrowNumber: target, PreviousGrowType: current, NewGrowType: packed, FirstGrowType: target, AwakeningStage: stage}, nil
}
