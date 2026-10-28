package progression

import (
	"context"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCommitExperienceKeepsUnchangedSkillLedgerWithoutWriting(t *testing.T) {
	for _, level := range []int{2, 90} {
		ctx := context.Background()
		repos := seedProgressionCommitRepositories(t, ctx)
		character, _, _ := repos.Character.Load(ctx, "77")
		skill, _, _ := repos.Skill.Load(ctx, "77")
		character.Level = level
		skill.Points.SyncedLevel = level
		skill.UpdatedAt = time.Unix(1234, 0).UTC()
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repos.Skill.Save(ctx, skill); err != nil {
			t.Fatal(err)
		}
		before, _, _ := repos.Skill.Load(ctx, "77")
		state := CommitState{Level: level, Experience: 240, SkillPoints: skill.Points}
		plan := CommitPlan{CharacterID: "77", AccountID: "dnf:1", Expected: state, Next: state}
		if level == 90 {
			plan.ExpectedCharacterStats = map[string]int64{"dungeon_capped_award_revision": 0}
			plan.NextCharacterStats = map[string]int64{"dungeon_capped_award_revision": 1}
		} else {
			plan.Next.Experience++
		}
		counted := &countingProgressionUnitOfWork{characters: repos.Character, skills: repos.Skill}
		result, err := Commit(ctx, counted, plan)
		if err != nil || !result.Applied || counted.characterSaves != 1 || counted.skillSaves != 0 {
			t.Fatal("经验变化仍应提交角色，未变化技能不写入", level, result, err, counted)
		}
		after, _, _ := repos.Skill.Load(ctx, "77")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("未变化技能及时间戳不应被改写")
		}
		result, err = Commit(ctx, counted, plan)
		if err != nil || !result.Idempotent || counted.characterSaves != 1 || counted.skillSaves != 0 {
			t.Fatal("重放不得写入", result, err)
		}
		// 同级SP/TP真有变化时仍须保存，不能用是否升级作唯一条件。
		plan.Expected = plan.Next
		plan.ExpectedCharacterStats = plan.NextCharacterStats
		plan.Next.SkillPoints.RemainingSP++
		plan.Next.SkillPoints.TotalSP++
		result, err = Commit(ctx, counted, plan)
		if err != nil || !result.Applied || counted.skillSaves != 1 {
			t.Fatal("技能点实际变化应提交", result, err)
		}
		current, _, _ := repos.Skill.Load(ctx, "77")
		if current.Points != plan.Next.SkillPoints {
			t.Fatal("技能点提交不一致", dnfrepo.CloneSkill(current))
		}
	}
}
