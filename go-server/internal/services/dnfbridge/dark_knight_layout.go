package dnfbridge

import (
	"context"
	"fmt"
	"maps"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

// 只调整黑武已有技能的布局；旧快捷键优先搬到后六格，不改加点、冷却与扩展资格。
func normalizeDarkKnightLayouts(ctx context.Context, repos dnfrepo.Group, catalog *dnfskill.Table, characterID string) (dnfrepo.SkillRecord, bool, error) {
	if repos.CharacterSkills == nil {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("黑武布局事务未就绪")
	}
	var result dnfrepo.SkillRecord
	changed := false
	err := repos.CharacterSkills.WithinCharacterSkill(ctx, characterID, func(skills dnfrepo.SkillRepository) error {
		current, found, err := skills.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("黑武技能记录不存在")
		}
		current = dnfrepo.CloneSkill(current)
		for index := 0; index < darkcombo.MaximumPageCount; index++ {
			// 初次布局仍由既有PVF初始技能入口构造，保留“最多前三主动”规则。
			if len(current.Layouts[index]) == 0 {
				continue
			}
			profile, available := darkcombo.LearnedProfile(current, index)
			if !available {
				continue
			}
			layout, err := skillcmd.BuildCurrentSkillLayout(catalog, darkcombo.Job, index, profile.Skills, current.Layouts[index])
			if err != nil {
				return err
			}
			if maps.Equal(current.Layouts[index], layout) {
				continue
			}
			if current.Layouts == nil {
				current.Layouts = make(map[int]dnfrepo.SkillLayout)
			}
			current.Layouts[index] = layout
			changed = true
		}
		if changed {
			current.UpdatedAt = time.Now().UTC()
			if err := dnfrepo.SaveSkillFields(ctx, skills, current, dnfrepo.SkillFieldLayouts); err != nil {
				return err
			}
		}
		result = current
		return nil
	})
	return result, changed, err
}
