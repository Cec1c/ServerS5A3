package skillcmd

import (
	"fmt"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// ExpandSkillTree 只准备分离副本，由商城同一个资产事务保存并扣费。
// 第一页保留原加点，第二页只包含运行时 PVF 的免费初始技能。
func ExpandSkillTree(character dnfrepo.CharacterRecord, record dnfrepo.SkillRecord, options OwnerOptions) (dnfrepo.SkillRecord, error) {
	if record.Trees.Expanded {
		return dnfrepo.SkillRecord{}, ErrSkillTreeAlreadyExpanded
	}
	if options.Catalog == nil || options.InitialLevels == nil || options.PointBaseline == nil {
		return dnfrepo.SkillRecord{}, ErrSkillRulesMissing
	}
	job, ok := characterJob(character)
	if !ok {
		return dnfrepo.SkillRecord{}, ErrSkillRulesMissing
	}
	record = dnfrepo.CloneSkill(record)
	record.CharacterID = character.CharacterID
	points, err := synchronizePointLedger(record.Points, options.PointBaseline, character.Level, options.Catalog, job, options.InitialLevels, record.Skills)
	if err != nil {
		return dnfrepo.SkillRecord{}, err
	}
	record.Points = points
	initial := make(map[int64]dnfrepo.SkillState, len(options.InitialLevels))
	for skillID, level := range options.InitialLevels {
		if level <= 0 {
			continue
		}
		definition, found := options.Catalog.Find(job, skillID)
		if !found {
			return dnfrepo.SkillRecord{}, fmt.Errorf("%w: initial skill=%d", ErrSkillNotFound, skillID)
		}
		if fixed := definition.FixedLevelForCharacter(character.Level); fixed > level {
			level = fixed
		}
		initial[int64(skillID)] = dnfrepo.SkillState{Level: level, Enabled: true}
	}
	if len(initial) == 0 {
		return dnfrepo.SkillRecord{}, ErrSkillRulesMissing
	}
	if record.Layouts == nil {
		record.Layouts = make(map[int]dnfrepo.SkillLayout)
	}
	if len(record.Skills) == 0 {
		record.Skills = dnfrepo.CloneSkill(dnfrepo.SkillRecord{Skills: initial}).Skills
		record.Points.RemainingSP, record.Points.RemainingTP = points.TotalSP, points.TotalTP
	}
	firstLayout, err := BuildCurrentSkillLayout(options.Catalog, job, 0, record.Skills, record.Layouts[0])
	if err != nil {
		return dnfrepo.SkillRecord{}, err
	}
	secondLayout, err := BuildInitialSkillLayout(options.Catalog, job, 1, initial)
	if err != nil {
		return dnfrepo.SkillRecord{}, err
	}
	otherPoints := points
	otherPoints.RemainingSP, otherPoints.RemainingTP = points.TotalSP, points.TotalTP
	record.Layouts[0], record.Layouts[1] = firstLayout, secondLayout
	record.Trees = dnfrepo.SkillTreeState{Expanded: true, Other: dnfrepo.SkillProfile{Skills: initial, Points: otherPoints}}
	return record, nil
}

func validateActiveSkillTree(record dnfrepo.SkillRecord, tree byte) error {
	if tree >= currentEXESkillTreeCount || record.Trees.Active > 1 || (!record.Trees.Expanded && record.Trees.Active != 0) {
		return ErrSkillTree
	}
	if tree == 1 && !record.Trees.Expanded {
		return ErrSkillTreeUnavailable
	}
	if int(tree) != dnfrepo.ActiveSkillTree(record) {
		return ErrSkillTreeStateMismatch
	}
	return nil
}

func characterJob(character dnfrepo.CharacterRecord) (byte, bool) {
	// 角色职业使用仓储原值，不能用缺省职业掩盖数据缺失。
	job, err := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
	return byte(job), err == nil
}

// 同步另一页的已获得总点数，同时保留它自己花掉的 SP/TP。
func SyncOtherSkillPoints(record *dnfrepo.SkillRecord) (bool, error) {
	if record == nil || !record.Trees.Expanded {
		return false, nil
	}
	points, changed, err := dnfskill.SyncPointState(record.Trees.Other.Points, record.Points)
	if err != nil {
		return false, err
	}
	record.Trees.Other.Points = points
	return changed, nil
}
