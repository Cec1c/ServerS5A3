package repository

import (
	"context"
	"time"
)

// SkillPointLedger 是独立的技能点投影，不是可整条保存的SkillRecord。
type SkillPointLedger struct {
	CharacterID string
	Points      SkillPointState
	UpdatedAt   time.Time
}

// 读写能力必须成对提供。保存只允许修改技能点及更新时间，不得覆盖技能、
// 布局、冷却或技能页；缺少此能力的仓储仍走原完整读取与保存流程。
type SkillPointLedgerRepository interface {
	LoadPointLedger(context.Context, string) (SkillPointLedger, bool, error)
	SavePointLedger(context.Context, SkillPointLedger) error
}
