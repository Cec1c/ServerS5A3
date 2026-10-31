package dnfbridge

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentDungeonCapAwardRevisionStatKey = "dungeon_capped_award_revision"

func (s *Service) commitCurrentDungeonExperiencePlan(ctx context.Context, repos dnfrepo.Group,
	plan progression.CommitPlan, maximumGains []uint32,
) (progression.CommitResult, error) {
	credit := false
	for _, gain := range maximumGains {
		credit = credit || gain > 0
	}
	config := s.adventureGroupTable.Runtime()
	if !credit || config.Capsule.MaximumExperience == 0 {
		return progression.Commit(ctx, repos.CharacterProgression, plan)
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return progression.CommitResult{}, err
	}
	return owner.CommitExperienceAward(ctx, dnfdungeon.ExperienceAwardCommand{
		Plan: plan, MaximumLevelGains: maximumGains, AdventureRuntime: config, UpdatedAt: time.Now().UTC(),
	})
}

// 同一段已接受死亡逐只规划，最终只保存一次角色、技能及满级账号进度。
// 事务失败整段回滚；不把预测快照发布到runtime或客户端，也不拆成逐只重试。
func (s *Service) commitCurrentDungeonExperienceBatch(ctx context.Context, session *gameSession,
	runtime *runtimeDungeonState, items []currentDungeonDeathBurstAward,
) ([]currentDungeonMonsterExperienceCommitResult, error) {
	timing := newCurrentDungeonSlowWorkTiming()
	defer timing.finish(s, session, "连续击杀经验分段耗时", [4]string{"角色技能读取毫秒", "逐只规划毫秒", "资产事务毫秒", "结果整理毫秒"})
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Skill == nil {
		return nil, errCurrentDungeonMonsterExperienceUnavailable
	}
	id := strconv.Itoa(int(session.selectedCharacterID))
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found || character.CharacterID != id || accountID == "" || strings.TrimSpace(character.AccountID) != accountID || runtime.Character.CharacterID != id {
		return nil, errCurrentDungeonMonsterExperienceStateInvalid
	}
	skill, found, err := loadCurrentDungeonExperienceSkillPoints(ctx, repos.Skill, id)
	if err != nil {
		return nil, err
	}
	exp, present := character.Stats["exp"]
	runtimeExp, runtimePresent := runtime.Character.Stats["exp"]
	if !found || skill.CharacterID != id || skill.Points.SyncedLevel != character.Level ||
		!present || !runtimePresent || exp < 0 || uint64(exp) > math.MaxUint32 || exp != runtimeExp || character.Level != runtime.Character.Level {
		return nil, errCurrentDungeonMonsterExperienceStateInvalid
	}
	timing.next()
	initial := dnfrepo.CloneCharacter(character)
	results := make([]currentDungeonMonsterExperienceCommitResult, len(items))
	var combined *progression.CommitPlan
	var maximumGains []uint32
	for i, item := range items {
		results[i] = item.result
		if item.result.Award.Gain == 0 {
			continue
		}
		result, plan, err := s.planCurrentDungeonExperience(ctx, item.tables, character, skill, item.result)
		if err != nil {
			return nil, err
		}
		results[i] = result
		maximumGains = append(maximumGains, result.HonorExpertGain)
		character, skill = result.Character, result.Skill
		if combined == nil {
			combined = &progression.CommitPlan{CharacterID: id, AccountID: accountID, Expected: plan.Expected,
				ExpectedCharacterStats: make(map[string]int64), NextCharacterStats: make(map[string]int64)}
		}
		combined.Next = plan.Next
		for key := range plan.ExpectedCharacterStats {
			combined.ExpectedCharacterStats[key] = initial.Stats[key]
		}
		for key, value := range plan.NextCharacterStats {
			combined.NextCharacterStats[key] = value
		}
	}
	if combined == nil {
		return results, nil
	}
	timing.next()
	committed, err := s.commitCurrentDungeonExperiencePlanTimed(ctx, session, repos, *combined, maximumGains)
	if err != nil {
		return nil, err
	}
	timing.next()
	for i := range results {
		results[i].Applied, results[i].Idempotent = committed.Applied, committed.Idempotent
		if s.adventureGroupTable.Runtime().Capsule.MaximumExperience > 0 {
			results[i].AdventureExperienceGain = results[i].HonorExpertGain
		}
	}
	return results, nil
}
