package progression

import (
	"context"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMaximumLevelNormalizationAndFreshHonorAwardCommitAtomically(t *testing.T) {
	ctx := context.Background()
	tables, err := Load(ctx, progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	repos := seedProgressionCommitRepositories(t, ctx)
	character, _, _ := repos.Character.Load(ctx, "77")
	skill, _, _ := repos.Skill.Load(ctx, "77")
	character.Level = 5
	character.Stats["exp"] = 1000
	character.Stats["honor_progress"] = 10
	skill.Points.SyncedLevel = 5
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	makePlan := func(exp uint32, honorBefore, honorAfter int64) CommitPlan {
		planned, err := tables.PlanExperienceAndSkillPoints(5, exp, 25, 5, skill.Points)
		if err != nil {
			t.Fatal(err)
		}
		planned.Experience, err = tables.LimitCharacterExperienceAtCap(planned.Experience, 5)
		if err != nil {
			t.Fatal(err)
		}
		plan := CommitPlanFromExperienceSkillPoints("77", character.AccountID, planned)
		plan.ExpectedCharacterStats = map[string]int64{"honor_progress": honorBefore}
		plan.NextCharacterStats = map[string]int64{"honor_progress": honorAfter}
		return plan
	}
	first := makePlan(1000, 10, 35)
	result, err := Commit(ctx, repos.CharacterProgression, first)
	if err != nil || !result.Applied || result.Idempotent {
		t.Fatalf("normalization=%+v err=%v", result, err)
	}
	second := makePlan(900, 35, 60)
	result, err = Commit(ctx, repos.CharacterProgression, second)
	if err != nil || !result.Applied || result.Idempotent {
		t.Fatalf("fresh honor award with unchanged EXP=%+v err=%v", result, err)
	}
	replay, err := Commit(ctx, repos.CharacterProgression, second)
	if err != nil || !replay.Idempotent || replay.Applied {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	stored, _, _ := repos.Character.Load(ctx, "77")
	storedSkill, _, _ := repos.Skill.Load(ctx, "77")
	if stored.Level != 5 || stored.Stats["exp"] != 900 || stored.Stats["honor_progress"] != 60 || storedSkill.Points != skill.Points {
		t.Fatalf("stored character=%+v skill=%+v", stored, storedSkill)
	}
	stale := makePlan(900, 35, 85)
	if _, err := Commit(ctx, repos.CharacterProgression, stale); !errors.Is(err, ErrCommitVersionPrecondition) {
		t.Fatalf("stale honor precondition=%v", err)
	}
}

func TestMaximumLevelProofCannotPermitArbitraryExperienceRegression(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"no PVF proof", "below cap floor", "wrong level", "changed previous EXP"} {
		t.Run(name, func(t *testing.T) {
			points := dnfrepo.SkillPointState{SyncedLevel: 5}
			planned, err := tables.PlanExperienceAndSkillPoints(5, 1000, 25, 5, points)
			if err != nil {
				t.Fatal(err)
			}
			planned.Experience, err = tables.LimitCharacterExperienceAtCap(planned.Experience, 5)
			if err != nil {
				t.Fatal(err)
			}
			plan := CommitPlanFromExperienceSkillPoints("77", "owner", planned)
			switch name {
			case "no PVF proof":
				plan.maximumLevelCap = nil
			case "below cap floor":
				plan.Next.Experience = 899
			case "wrong level":
				plan.Expected.Level = 4
				plan.Expected.SkillPoints.SyncedLevel = 4
			case "changed previous EXP":
				plan.Expected.Experience = 800
				plan.Next.Experience = 700
			}
			if err := validateCommitPlan(plan); !errors.Is(err, ErrCommitPlanInvalid) {
				t.Fatalf("unproved regression accepted: err=%v plan=%+v", err, plan)
			}
		})
	}
}

func TestUnchangedProgressionRejectsUnversionedCharacterStats(t *testing.T) {
	plan := validProgressionCommitPlan()
	plan.Expected = plan.Next
	plan.NextCharacterStats = map[string]int64{"honor_progress": 10}
	if err := validateCommitPlan(plan); !errors.Is(err, ErrCommitPlanInvalid) {
		t.Fatalf("unversioned stat transition accepted: %v", err)
	}
}
