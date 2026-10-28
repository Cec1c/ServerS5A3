package progression

import (
	"context"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type readOnlyPointProjection struct {
	repo.SkillRepository
	fullReads, fullSaves, pointReads int
}

func (s *readOnlyPointProjection) Load(ctx context.Context, id string) (repo.SkillRecord, bool, error) {
	s.fullReads++
	return s.SkillRepository.Load(ctx, id)
}
func (s *readOnlyPointProjection) Save(ctx context.Context, r repo.SkillRecord) error {
	s.fullSaves++
	return s.SkillRepository.Save(ctx, r)
}
func (s *readOnlyPointProjection) LoadPointLedger(context.Context, string) (repo.SkillPointLedger, bool, error) {
	s.pointReads++
	return repo.SkillPointLedger{}, false, nil
}

type directPointLedgerUnitOfWork struct {
	characters repo.CharacterRepository
	skills     repo.SkillRepository
}

func (u directPointLedgerUnitOfWork) WithinCharacterProgression(_ context.Context, _ string, apply func(repo.CharacterRepository, repo.SkillRepository) error) error {
	return apply(u.characters, u.skills)
}

func TestCommitReadOnlyPointProjectionFallsBackWithoutLosingSkillData(t *testing.T) {
	ctx := context.Background()
	g := seedProgressionCommitRepositories(t, ctx)
	before, _, _ := g.Skill.Load(ctx, "77")
	before.Layouts = map[int]repo.SkillLayout{0: {1: 46}}
	before.Trees = repo.SkillTreeState{Expanded: true, Active: 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{50: {Level: 3}}}}
	if err := g.Skill.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	skill := &readOnlyPointProjection{SkillRepository: g.Skill}
	u := directPointLedgerUnitOfWork{characters: g.Character, skills: skill}
	result, err := Commit(ctx, u, validProgressionCommitPlan())
	if err != nil || !result.Applied {
		t.Fatal(result, err)
	}
	if skill.pointReads != 0 || skill.fullReads != 1 || skill.fullSaves != 1 {
		t.Fatal("只有窄读能力时必须保持原完整路径", skill)
	}
	after, _, _ := g.Skill.Load(ctx, "77")
	after.Points = before.Points
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("完整仓储回退丢失了技能明细", before, after)
	}
}

type failingPointLedger struct {
	*readOnlyPointProjection
	ledger           repo.SkillPointLedger
	found            bool
	loadErr, saveErr error
	pointSaves       int
}

func (s *failingPointLedger) LoadPointLedger(context.Context, string) (repo.SkillPointLedger, bool, error) {
	s.pointReads++
	return s.ledger, s.found, s.loadErr
}
func (s *failingPointLedger) SavePointLedger(context.Context, repo.SkillPointLedger) error {
	s.pointSaves++
	return s.saveErr
}

func TestCommitPointLedgerErrorsNeverFallbackOrOverwriteFullSkill(t *testing.T) {
	fixtureErr := errors.New("窄账本故障夹具")
	for _, scenario := range []string{"读取失败", "记录不存在", "角色归属错误", "技能点过期", "写入失败", "重试已提交"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			g := seedProgressionCommitRepositories(t, ctx)
			plan := validProgressionCommitPlan()
			skill := &failingPointLedger{readOnlyPointProjection: &readOnlyPointProjection{SkillRepository: g.Skill}, ledger: repo.SkillPointLedger{CharacterID: plan.CharacterID, Points: plan.Expected.SkillPoints}, found: true}
			want := fixtureErr
			switch scenario {
			case "读取失败":
				skill.loadErr = fixtureErr
			case "记录不存在":
				skill.found = false
				want = ErrCommitStateMissing
			case "角色归属错误":
				skill.ledger.CharacterID = "other"
				want = ErrCommitOwnershipMismatch
			case "技能点过期":
				skill.ledger.Points.RemainingSP--
				want = ErrCommitVersionPrecondition
			case "写入失败":
				skill.saveErr = fixtureErr
			case "重试已提交":
				c, _, _ := g.Character.Load(ctx, plan.CharacterID)
				c.Level = plan.Next.Level
				c.Stats["exp"] = int64(plan.Next.Experience)
				if err := g.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
				skill.ledger.Points = plan.Next.SkillPoints
				want = nil
			}
			u := directPointLedgerUnitOfWork{characters: g.Character, skills: skill}
			result, err := Commit(ctx, u, plan)
			if !errors.Is(err, want) {
				t.Fatalf("错误=%v，预期=%v", err, want)
			}
			if skill.pointReads != 1 || skill.fullReads != 0 || skill.fullSaves != 0 {
				t.Fatal("窄读失败不得改走完整保存", skill)
			}
			if scenario == "写入失败" {
				if skill.pointSaves != 1 {
					t.Fatal("未尝试窄写")
				}
			} else if skill.pointSaves != 0 {
				t.Fatal("不应写技能点", skill.pointSaves)
			}
			if scenario == "重试已提交" && (!result.Idempotent || result.Applied) {
				t.Fatal("重复请求未保持幂等", result)
			}
		})
	}
}
