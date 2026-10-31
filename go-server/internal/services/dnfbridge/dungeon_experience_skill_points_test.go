package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type experiencePlanningFullSkill struct {
	dnfrepo.SkillRepository
	loads int
}

func (p *experiencePlanningFullSkill) Load(ctx context.Context, id string) (dnfrepo.SkillRecord, bool, error) {
	p.loads++
	return p.SkillRepository.Load(ctx, id)
}

type experiencePlanningPointSkill struct {
	*experiencePlanningFullSkill
	pointLoads int
	loadError  error
	missing    bool
}

func (p *experiencePlanningPointSkill) LoadPointLedger(ctx context.Context, id string) (dnfrepo.SkillPointLedger, bool, error) {
	p.pointLoads++
	if p.loadError != nil || p.missing {
		return dnfrepo.SkillPointLedger{}, false, p.loadError
	}
	record, found, err := p.SkillRepository.Load(ctx, id)
	return dnfrepo.SkillPointLedger{CharacterID: record.CharacterID, Points: record.Points, UpdatedAt: record.UpdatedAt}, found, err
}

func (p *experiencePlanningPointSkill) SavePointLedger(context.Context, dnfrepo.SkillPointLedger) error {
	return errors.New("规划阶段不允许保存技能投影")
}

func TestDungeonExperiencePlanningLedgerMatchesFullReadAndPreservesSkills(t *testing.T) {
	for _, capped := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			name := "未满级单次"
			if capped {
				name = "满级单次"
			}
			if batch {
				name += "批次"
			}
			t.Run(name, func(t *testing.T) {
				var expectedCharacter dnfrepo.CharacterRecord
				var expectedSkill dnfrepo.SkillRecord
				var expectedResults []currentDungeonMonsterExperienceCommitResult
				for _, narrow := range []bool{false, true} {
					s, session, repos, _ := prepareDungeonDeathBurstTest(t, capped)
					ctx := context.Background()
					full, _, _ := repos.Skill.Load(ctx, "99")
					full.Skills = map[int64]dnfrepo.SkillState{40: {Level: 3, Enabled: true}}
					full.Layouts = map[int]dnfrepo.SkillLayout{0: {3: 40}}
					full.Cooldowns = map[int64]time.Time{40: time.Unix(20000, 0).UTC()}
					full.Trees = dnfrepo.SkillTreeState{Expanded: true, Active: 1, Other: dnfrepo.SkillProfile{Skills: map[int64]dnfrepo.SkillState{41: {Level: 2}}, Points: full.Points}}
					if err := repos.Skill.Save(ctx, full); err != nil {
						t.Fatal(err)
					}
					probe := &experiencePlanningPointSkill{experiencePlanningFullSkill: &experiencePlanningFullSkill{SkillRepository: repos.Skill}}
					if narrow {
						repos.Skill = probe
					} else {
						repos.Skill = probe.experiencePlanningFullSkill
					}
					s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
					resources, err := s.currentDungeonExperienceCommitResources()
					if err != nil {
						t.Fatal(err)
					}
					award := currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 10}}
					var results []currentDungeonMonsterExperienceCommitResult
					if batch {
						items := make([]currentDungeonDeathBurstAward, 3)
						for i := range items {
							items[i] = currentDungeonDeathBurstAward{result: award, tables: resources.Progression}
						}
						results, err = s.commitCurrentDungeonExperienceBatch(ctx, session, session.dungeon.runtime, items)
					} else {
						var result currentDungeonMonsterExperienceCommitResult
						result, err = s.commitCurrentDungeonExperience(ctx, session, session.dungeon.runtime, resources.Progression, award)
						results = append(results, result)
					}
					if err != nil {
						t.Fatal(err)
					}
					if narrow && (probe.pointLoads != 1 || probe.loads != 0) || !narrow && (probe.pointLoads != 0 || probe.loads != 1) {
						t.Fatal("规划阶段的读取路径不匹配", probe.pointLoads, probe.loads)
					}
					character, _, err := repos.Character.Load(ctx, "99")
					if err != nil {
						t.Fatal(err)
					}
					stored, _, err := probe.SkillRepository.Load(ctx, "99")
					if err != nil || !reflect.DeepEqual(full.Skills, stored.Skills) || !reflect.DeepEqual(full.Layouts, stored.Layouts) || !reflect.DeepEqual(full.Cooldowns, stored.Cooldowns) || !reflect.DeepEqual(full.Trees, stored.Trees) {
						t.Fatal("经验规划或提交影响了技能、布局、冷却或双技能页", err)
					}
					character.UpdatedAt, stored.UpdatedAt = time.Time{}, time.Time{}
					if !narrow {
						expectedCharacter, expectedSkill, expectedResults = character, stored, results
					} else if !reflect.DeepEqual(expectedCharacter, character) || !reflect.DeepEqual(expectedSkill, stored) || !reflect.DeepEqual(expectedResults, results) {
						t.Fatal("窄规划与完整规划的角色、技能、经验或熟练度结果不同")
					}
				}
			})
		}
	}
}

func TestDungeonExperiencePlanningLedgerDoesNotFallbackOnReadFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		s, session, repos, _ := prepareDungeonDeathBurstTest(t, false)
		probe := &experiencePlanningPointSkill{experiencePlanningFullSkill: &experiencePlanningFullSkill{SkillRepository: repos.Skill}, missing: missing}
		if !missing {
			probe.loadError = errors.New("技能点读取失败")
		}
		repos.Skill = probe
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
		ctx := context.Background()
		before, _, _ := repos.Character.Load(ctx, "99")
		resources, _ := s.currentDungeonExperienceCommitResources()
		_, err := s.commitCurrentDungeonExperience(ctx, session, session.dungeon.runtime, resources.Progression, currentDungeonMonsterExperienceCommitResult{Award: currentDungeonMonsterExperienceAward{Gain: 10}})
		after, _, _ := repos.Character.Load(ctx, "99")
		if err == nil || probe.pointLoads != 1 || probe.loads != 0 || !reflect.DeepEqual(before, after) {
			t.Fatal("窄读取错误或缺失不能回退掩盖，角色不能变化", err, probe.pointLoads, probe.loads)
		}
	}
}
