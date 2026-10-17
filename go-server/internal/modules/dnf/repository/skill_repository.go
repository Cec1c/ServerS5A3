// 本文件定义 DNF 技能仓储接口、记录和字段保存入口。
package repository

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/platform/db"
)

// SkillRepository 保存角色技能等级和冷却状态。
type SkillRepository interface {
	db.Store[SkillRecord]
}

// SkillRecord 是角色技能仓储记录。
type SkillRecord struct {
	CharacterID string               `json:"character_id"`
	Skills      map[int64]SkillState `json:"skills,omitempty"`
	Points      SkillPointState      `json:"points,omitempty"`
	Layouts     map[int]SkillLayout  `json:"layouts,omitempty"`
	Trees       SkillTreeState       `json:"trees,omitempty"`
	Cooldowns   map[int64]time.Time  `json:"cooldowns,omitempty"`
	UpdatedAt   time.Time            `json:"updated_at,omitempty"`
}

// SkillTreeState 保存扩展资格、当前页及另一页的独立加点。
// Skills/Points 始终是当前生效页，供战斗、装备校验和升级等既有入口使用。
// 冷却属于角色，两页共用，切页不能清除冷却。
type SkillTreeState struct {
	Expanded bool         `json:"expanded,omitempty"`
	Active   byte         `json:"active,omitempty"`
	Other    SkillProfile `json:"other,omitempty"`
}

type SkillProfile struct {
	Skills map[int64]SkillState `json:"skills,omitempty"`
	Points SkillPointState      `json:"points,omitempty"`
}

// ActiveSkillTree 对历史默认页码不授予购买资格；原有技能保持为第一页。
func ActiveSkillTree(record SkillRecord) int {
	if record.Trees.Expanded && record.Trees.Active == 1 {
		return 1
	}
	return 0
}

// SkillTreeWireIndex 的 0xff 表示尚未购买第二套加点。
func SkillTreeWireIndex(record SkillRecord) byte {
	if !record.Trees.Expanded || record.Trees.Active > 1 {
		return 0xff
	}
	return byte(ActiveSkillTree(record))
}

// SkillState 是单个技能的可变状态。
type SkillState struct {
	Level   int  `json:"level,omitempty"`
	Enabled bool `json:"enabled,omitempty"`
}

// SkillPointState is the authoritative per-character SP/TP ledger.
type SkillPointState struct {
	TotalSP     int `json:"total_sp,omitempty"`
	RemainingSP int `json:"remaining_sp,omitempty"`
	TotalTP     int `json:"total_tp,omitempty"`
	RemainingTP int `json:"remaining_tp,omitempty"`
	SyncedLevel int `json:"synced_level,omitempty"`
}

// SkillLayout maps a current-EXE skill UI slot to a job-scoped skill ID.
type SkillLayout map[int]uint16

// SkillField 表示技能记录可局部保存的字段。
type SkillField string

const (
	SkillFieldSkills    SkillField = "skills"
	SkillFieldPoints    SkillField = "points"
	SkillFieldLayouts   SkillField = "layouts"
	SkillFieldCooldowns SkillField = "cooldowns"
	SkillFieldTrees     SkillField = "trees"
)

// SaveSkillFields 保存技能指定字段；底层不支持局部保存时退化为整条保存。
func SaveSkillFields(ctx context.Context, repo SkillRepository, record SkillRecord, fields ...SkillField) error {
	return db.SaveFields(ctx, repo, record, SkillFields.Normalize, fields...)
}

// CloneSkill 拷贝技能记录，避免技能表和冷却表与调用方共享。
func CloneSkill(record SkillRecord) SkillRecord {
	record.Skills = cloneSkillMap(record.Skills)
	record.Trees.Other.Skills = cloneSkillMap(record.Trees.Other.Skills)
	record.Layouts = cloneSkillLayouts(record.Layouts)
	record.Cooldowns = cloneTimeMap(record.Cooldowns)
	return record
}

func cloneSkillLayouts(layouts map[int]SkillLayout) map[int]SkillLayout {
	if layouts == nil {
		return nil
	}
	out := make(map[int]SkillLayout, len(layouts))
	for tree, layout := range layouts {
		copyLayout := make(SkillLayout, len(layout))
		for slot, skillID := range layout {
			copyLayout[slot] = skillID
		}
		out[tree] = copyLayout
	}
	return out
}

func SkillKey(record SkillRecord) string {
	return strings.TrimSpace(record.CharacterID)
}
