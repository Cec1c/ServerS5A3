// 本文件实现"已保存技能等级高于当前客户端可学习上限"的登录修复。
//
// 现场症状（操作者原话）："回退技能加点显示已精通该技能"，技能点重置后某个技能
// 显示已精通、加不了点。客户端把"已精通"画在 `当前等级 >= 可学习上限` 上，而那个
// 上限来自 PVF `[growtype maximum level]`，不是 `[maximum level]`：实测真档
// 上挑 `[maximum level]=20 / [growtype maximum level]=10`、物理暴击 20/10、
// 基础精通 100/1。旧实现只把 `[maximum level]` 当上限，因此任何高于该方向上上限
// 的已保存等级都会被客户端一直画成"已精通"且 "+" 永久变灰。
//
// 修复分两半：
//  1. 购买门禁按客户端上限走（skillcmd/owner.go 的加分分支）；
//  2. 本文件在登录技能同步边界把历史遗留的超限行夹回上限，并按同一套点数规则
//     退还 SP/TP，让被卡住的技能重新可加点。
//
// 初始免费授予及 [fixed level skill] 自动成长等级不属于玩家购买等级；
// 它们可以高于手动可学上限，登录修复须保留当前等级的真实PVF免费基线。
//
// 安全边界：只夹 `[growtype maximum level][当前方向] > 0` 的技能。方向上限为 0
// 表示"该方向不可学"，它既可能是历史脏数据，也可能是我们尚未实现的转职流程留下的
// grow_type=0；后者夹成 0 会直接删掉玩家的技能，因此只上报、绝不改写。
package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// LevelCapClamp 是一个被夹回可学习上限的技能行。
type LevelCapClamp struct {
	SkillID      uint16
	From         int
	To           int
	MaxLearnable int
}

// LevelCapRepairCommand carries the caller-owned character facts the PVF cap
// needs: the catalog is the owner's table, so only the direction and level are
// passed in.
type LevelCapRepairCommand struct {
	CharacterID    string
	Job            byte
	GrowType       int
	CharacterLevel int
	InitialLevels  map[uint16]int
	UpdatedAt      time.Time
	// ObserveOnly 只报告、不落库：返回的 record 是**未改动**的原记录，Clamps
	// 仍然照常填充。这是 DNFBRIDGE_SKILL_CAP_REPAIR=observe 的 A/B 档 —— 夹回
	// 上限会真实删掉玩家等级（只退 SP），所以现场必须先有一次"只看不动"的机会。
	ObserveOnly bool
}

// LevelCapRepairResult 是登录修复的结果。
type LevelCapRepairResult struct {
	CharacterID string
	Clamps      []LevelCapClamp
	// Unavailable lists learned skills whose direction cap is 0. They are
	// reported, never rewritten (see the file header).
	Unavailable []uint16
}

// Changed reports whether the repair persisted anything.
func (r LevelCapRepairResult) Changed() bool { return len(r.Clamps) > 0 }

// SkillAtCapEntry is one learned skill sitting exactly on the client's
// learnable cap: those are the rows the client draws as 已精通.
type SkillAtCapEntry struct {
	SkillID      uint16
	Level        int
	MaxLearnable int
}

// RepairLevelCaps 只下调超过手动可学上限及PVF免费基线的已保存技能等级，
// 并退还差额SP/TP；不主动升级技能，也不改变购买门禁。
func (o *SceneOwner) RepairLevelCaps(ctx context.Context, table *Table, command LevelCapRepairCommand) (dnfrepo.SkillRecord, LevelCapRepairResult, error) {
	characterID := strings.TrimSpace(command.CharacterID)
	if characterID == "" {
		return dnfrepo.SkillRecord{}, LevelCapRepairResult{}, ErrCharacterRequired
	}
	if table == nil {
		return dnfrepo.SkillRecord{}, LevelCapRepairResult{}, fmt.Errorf("%w: %s", ErrSkillRecordMissing, "skill catalog")
	}
	if o == nil || o.skills == nil {
		return dnfrepo.SkillRecord{}, LevelCapRepairResult{}, ErrSceneOwnerUnavailable
	}
	ctx = normalizedSceneContext(ctx)
	if err := ctx.Err(); err != nil {
		return dnfrepo.SkillRecord{}, LevelCapRepairResult{}, err
	}
	result := LevelCapRepairResult{CharacterID: characterID}
	selected := dnfrepo.SkillRecord{}
	err := o.skills.WithinCharacterSkill(ctx, characterID, func(skills dnfrepo.SkillRepository) error {
		current, found, err := skills.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || len(current.Skills) == 0 {
			return nil
		}
		current = dnfrepo.CloneSkill(current)
		original := dnfrepo.CloneSkill(current)
		ids := make([]int, 0, len(current.Skills))
		for rawID := range current.Skills {
			if rawID >= 0 && rawID <= 0xffff {
				ids = append(ids, int(rawID))
			}
		}
		sort.Ints(ids)
		layoutTouched := false
		for _, rawID := range ids {
			skillID := uint16(rawID)
			state := current.Skills[int64(skillID)]
			if state.Level <= 0 {
				continue
			}
			definition, ok := table.Find(command.Job, skillID)
			if !ok {
				continue
			}
			maxLearnable := definition.MaxLearnableLevel(command.GrowType, command.CharacterLevel)
			if maxLearnable <= 0 {
				result.Unavailable = append(result.Unavailable, skillID)
				continue
			}
			target := maxLearnable
			// 初始免费及自动成长授予可高于玩家购买上限。先确定完整免费基线，
			// 再判断是否超限，避免转职/洗点后下一次投影又降低合法技能或反复保存。
			if floor := command.InitialLevels[skillID]; floor > target {
				target = floor
			}
			if automatic := definition.FixedLevelForCharacter(command.CharacterLevel); automatic > target {
				target = automatic
			}
			if state.Level <= target {
				continue
			}
			if target < 0 {
				target = 0
			}
			delta, err := levelCostDelta(definition, state.Level, target)
			if err != nil {
				return err
			}
			if definition.IsTPSkill() {
				current.Points.RemainingTP -= delta
				if current.Points.RemainingTP > current.Points.TotalTP {
					current.Points.RemainingTP = current.Points.TotalTP
				}
			} else {
				current.Points.RemainingSP -= delta
				if current.Points.RemainingSP > current.Points.TotalSP {
					current.Points.RemainingSP = current.Points.TotalSP
				}
			}
			result.Clamps = append(result.Clamps, LevelCapClamp{
				SkillID:      skillID,
				From:         state.Level,
				To:           target,
				MaxLearnable: maxLearnable,
			})
			if target <= 0 {
				delete(current.Skills, int64(skillID))
				for tree, layout := range current.Layouts {
					if tree != dnfrepo.ActiveSkillTree(current) {
						continue
					}
					for slot, slotted := range layout {
						if slotted == skillID {
							delete(layout, slot)
							layoutTouched = true
						}
					}
				}
				continue
			}
			current.Skills[int64(skillID)] = dnfrepo.SkillState{Level: target, Enabled: true}
		}
		if !result.Changed() {
			selected = dnfrepo.CloneSkill(current)
			return nil
		}
		if command.ObserveOnly {
			// 报告档：把夹过的内存态丢掉，交回未改动的原记录，一个字段都不落库。
			selected = dnfrepo.CloneSkill(original)
			return nil
		}
		fields := []dnfrepo.SkillField{dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints}
		if layoutTouched {
			fields = append(fields, dnfrepo.SkillFieldLayouts)
		}
		current.UpdatedAt = normalizedSceneTime(command.UpdatedAt)
		if err := dnfrepo.SaveSkillFields(ctx, skills, current, fields...); err != nil {
			return err
		}
		selected = dnfrepo.CloneSkill(current)
		return nil
	})
	if err != nil {
		return dnfrepo.SkillRecord{}, LevelCapRepairResult{}, err
	}
	return selected, result, nil
}

// SkillsAtLearnableCap reports the learned skills that sit exactly on the
// client's learnable cap. Those rows are what the client renders as 已精通, so
// the list identifies the skill behind an operator report without guessing.
func SkillsAtLearnableCap(table *Table, job byte, growType, characterLevel int, states map[int64]dnfrepo.SkillState) []SkillAtCapEntry {
	if table == nil || len(states) == 0 {
		return nil
	}
	out := make([]SkillAtCapEntry, 0, 8)
	for rawID, state := range states {
		if state.Level <= 0 || rawID < 0 || rawID > 0xffff {
			continue
		}
		skillID := uint16(rawID)
		definition, ok := table.Find(job, skillID)
		if !ok {
			continue
		}
		maxLearnable := definition.MaxLearnableLevel(growType, characterLevel)
		if maxLearnable <= 0 || state.Level != maxLearnable {
			continue
		}
		out = append(out, SkillAtCapEntry{SkillID: skillID, Level: state.Level, MaxLearnable: maxLearnable})
	}
	sort.Slice(out, func(left, right int) bool { return out[left].SkillID < out[right].SkillID })
	return out
}

// SkillsWithUnavailableDirection reports the learned skills the character's
// current grow_type direction cannot learn at all. The maximum-level tables are
// the PVF owner; `[skill fitness growtype]` is metadata and does not gate a job.
//
// 与 RepairLevelCaps 的 result.Unavailable 的区别：这里只看**方向**规则，
// 不把"等级还不够"混进来。用途是把"持久化方向"和"持久化技能"之间的矛盾
// 直接点名 —— 例如一个已经从剑魂学了 27/33/110 的角色，权威列却写着
// grow_type=0（未转职）：门禁会拒绝这些技能的每一次加点，而这个列表就是
// 那条"数据与技能自相矛盾"的判据。
func SkillsWithUnavailableDirection(table *Table, job byte, growType int, states map[int64]dnfrepo.SkillState) []uint16 {
	if table == nil || len(states) == 0 {
		return nil
	}
	out := make([]uint16, 0, 8)
	for rawID, state := range states {
		if state.Level <= 0 || rawID < 0 || rawID > 0xffff {
			continue
		}
		skillID := uint16(rawID)
		definition, ok := table.Find(job, skillID)
		if !ok {
			continue
		}
		if definition.SupportsCharacterGrowth(growType) {
			continue
		}
		out = append(out, skillID)
	}
	sort.Slice(out, func(left, right int) bool { return out[left] < out[right] })
	return out
}

// levelCostDelta mirrors the skill owner's point accounting: positive when the
// level is raised, negative when it is lowered.
func levelCostDelta(definition Skill, currentLevel int, targetLevel int) (int, error) {
	total := 0
	if targetLevel > currentLevel {
		for level := currentLevel; level < targetLevel; level++ {
			cost := definition.LevelCost(level)
			if cost < 0 {
				return 0, fmt.Errorf("skill=%d level=%d cost=%d", definition.ID, level, cost)
			}
			total += cost
		}
		return total, nil
	}
	for level := currentLevel - 1; level >= targetLevel; level-- {
		cost := definition.LevelCost(level)
		if cost < 0 {
			return 0, fmt.Errorf("skill=%d level=%d cost=%d", definition.ID, level, cost)
		}
		total -= cost
	}
	return total, nil
}
