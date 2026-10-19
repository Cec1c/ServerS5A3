package skillcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPVPProfileSwitchKeepsOrdinaryPageAndCharacterIndex(t *testing.T) {
	ctx := context.Background()
	r := seededSkillOwnerRepositories(t, ctx)
	unlockSkillOwnerFixture(t, ctx, r)
	ordinary, _, _ := r.Skill.Load(ctx, "31")
	character, _, _ := r.Character.Load(ctx, "31")
	character.Stats["skill_tree_index"] = 0
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	pk, err := PreparePVPSkills(ctx, r, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(PVPRepositories(r, "acc-skill", "31"), OwnerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyTreeSwitch(ctx, NewTreeCommand(alignedRequest("acc-skill", 31), ChangeAnotherSkillTreeRequest{SkillTree: 0}))
	if err != nil || result.Target != 1 {
		t.Fatalf("PK切页: %+v %v", result, err)
	}
	actual, _, _ := r.Skill.Load(ctx, "31")
	afterCharacter, _, _ := r.Character.Load(ctx, "31")
	reloaded, err := PreparePVPSkills(ctx, r, "acc-skill", "31")
	if err != nil || repo.ActiveSkillTree(reloaded) != 1 {
		t.Fatalf("PK活动页未保存: %v", err)
	}
	if !reflect.DeepEqual(actual, ordinary) || afterCharacter.Stats["skill_tree_index"] != 0 {
		t.Fatal("PK切页改变刷图技能或活动页")
	}
	beforePage, _ := repo.SkillTree(pk, 1)
	afterPage, _ := repo.SkillTree(reloaded, 1)
	if !reflect.DeepEqual(beforePage, afterPage) {
		t.Fatal("切页复制覆盖了另一页")
	}
}

func TestPVPCopyCurrentPagePreservesOtherPageAndRollsBack(t *testing.T) {
	ctx := context.Background()
	r := seededSkillOwnerRepositories(t, ctx)
	unlockSkillOwnerFixture(t, ctx, r)
	before, _, _ := r.Skill.Load(ctx, "31")
	pk, err := PreparePVPSkills(ctx, r, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	pk.Skills[46] = repo.SkillState{Level: 4, Enabled: true}
	pk.Points.RemainingSP = 40
	pk.Points.TotalTP, pk.Points.RemainingTP = 6, 2
	pk.Layouts = map[int]repo.SkillLayout{0: {198: 46}, 1: {199: 46}}
	if err := PVPRepositories(r, "acc-skill", "31").CharacterSkills.WithinCharacterSkill(ctx, "31", func(store repo.SkillRepository) error { return store.Save(ctx, pk) }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("复制后提交失败")
	apply := func(fail bool) error {
		return r.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
			if err := CopyPVPActivePageToOrdinary(ctx, tx, "acc-skill", "31"); err != nil {
				return err
			}
			if fail {
				return failure
			}
			return nil
		})
	}
	if err := apply(true); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	rolled, _, _ := r.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(before, rolled) {
		t.Fatal("失败事务未回滚普通技能")
	}
	if err := apply(false); err != nil {
		t.Fatal(err)
	}
	after, _, _ := r.Skill.Load(ctx, "31")
	if after.Skills[46].Level != 4 || after.Points.RemainingSP != 40 || after.Layouts[0][198] != 46 {
		t.Fatal("未复制当前页加点与布局")
	}
	if after.Points.TotalTP != 10 || after.Points.RemainingTP != 6 {
		t.Fatal("复制丢失了已获得的新TP或改变源已消费量")
	}
	if !reflect.DeepEqual(after.Trees, before.Trees) || !reflect.DeepEqual(after.Layouts[1], before.Layouts[1]) {
		t.Fatal("复制覆盖了目标另一页")
	}
	// 复制之后修改普通页，再读取PK存档，不能形成持续同步或共享引用。
	after.Skills[46] = repo.SkillState{Level: 2, Enabled: true}
	after.Layouts[0][198] = 47
	if err := r.Skill.Save(ctx, after); err != nil {
		t.Fatal(err)
	}
	reloaded, err := PreparePVPSkills(ctx, r, "acc-skill", "31")
	if err != nil || reloaded.Skills[46].Level != 4 || reloaded.Layouts[0][198] != 46 {
		t.Fatal("普通页修改串到PK技能", err)
	}
}
