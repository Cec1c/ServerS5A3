package skillcmd

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestDojoSkillsBuyResetReloadPreservesOrdinarySkills(t *testing.T) {
	ctx := context.Background()
	base := seededSkillOwnerRepositories(t, ctx)
	ordinary, _, _ := base.Skill.Load(ctx, "31")
	ordinary.Layouts = map[int]repo.SkillLayout{0: {0: 46}}
	ordinary.Cooldowns = map[int64]time.Time{46: time.Unix(123, 0)}
	if err := base.Skill.Save(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	view, err := PrepareDojoSkills(ctx, base, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Trees.Expanded || view.Trees.Other.Skills[46].Level != 1 {
		t.Fatalf("初始化 %+v", view)
	}
	options := OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}, PointBaseline: &ordinary.Points}
	owner, err := NewOwner(DojoRepositories(base, "acc-skill", "31"), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}}})); err != nil {
		t.Fatal(err)
	}
	next, err := PrepareDojoSkills(ctx, base, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	if next.Skills[46].Level != 2 || next.Points.RemainingSP != 80 || next.Trees.Other.Skills[46].Level != 1 {
		t.Fatalf("独立加点 %+v", next)
	}
	second, err := NewOwner(DojoRepositories(base, "acc-skill", "31", 1), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 1, Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 2}}})); err != nil {
		t.Fatal(err)
	}
	pages, err := PrepareDojoSkills(ctx, base, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := repo.SkillTree(pages, 0)
	other, _ := repo.SkillTree(pages, 1)
	if first.Skills[46].Level != 2 || other.Skills[46].Level != 3 || other.Points.RemainingSP != 60 {
		t.Fatal("独立道场两页发生串写")
	}
	actual, _, _ := base.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(actual, ordinary) {
		t.Fatalf("道场覆盖普通技能: %+v", actual)
	}
	// 用新owner模拟重新登录，重置仍只作用于独立加点。
	owner, _ = NewOwner(DojoRepositories(base, "acc-skill", "31", 0), options)
	if _, err := owner.ApplyReset(ctx, NewResetCommand(alignedRequest("acc-skill", 31), SkillInitRequest{})); err != nil {
		t.Fatal(err)
	}
	reset, err := PrepareDojoSkills(ctx, base, "acc-skill", "31")
	if err != nil {
		t.Fatal(err)
	}
	if reset.Skills[46].Level != 1 || reset.Points.RemainingSP != 100 || reset.Trees.Other.Skills[46].Level != 3 || !reflect.DeepEqual(reset.Cooldowns, ordinary.Cooldowns) {
		t.Fatalf("道场重置 %+v", reset)
	}
	actual, _, _ = base.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(actual, ordinary) {
		t.Fatal("独立重置覆盖普通技能")
	}
}

func TestDojoSkillsRejectForeignAndCorruptProfiles(t *testing.T) {
	ctx := context.Background()
	base := seededSkillOwnerRepositories(t, ctx)
	if _, err := PrepareDojoSkills(ctx, base, "foreign", "31"); !errors.Is(err, ErrCharacterOwner) {
		t.Fatalf("跨账号: %v", err)
	}
	if err := base.Settings.Save(ctx, repo.SettingsRecord{Scope: DojoSkillScope("31"), Values: map[string]string{"profile": "invalid"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDojoSkills(ctx, base, "acc-skill", "31"); err == nil {
		t.Fatal("损坏的独立加点被静默覆盖")
	}
	if err := base.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
		return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: DojoSkillScope("32"), Values: map[string]string{"profile": "bad"}})
	}); err == nil {
		t.Fatal("结算事务允许其他角色道场配置")
	}
}

func TestDojoSkillsTransactionRollsBackProfileAndAssets(t *testing.T) {
	ctx := context.Background()
	base := seededSkillOwnerRepositories(t, ctx)
	if _, err := PrepareDojoSkills(ctx, base, "acc-skill", "31"); err != nil {
		t.Fatal(err)
	}
	before, _, _ := base.Settings.Load(ctx, DojoSkillScope("31"))
	ordinary, _, _ := base.Skill.Load(ctx, "31")
	failure := errors.New("测试事务回滚")
	scoped := DojoRepositories(base, "acc-skill", "31")
	err := scoped.CharacterSettlement.WithinCharacterSettlement(ctx, "31", func(tx repo.Group) error {
		record, _, err := tx.Skill.Load(ctx, "31")
		if err != nil {
			return err
		}
		record.Skills[46] = repo.SkillState{Level: 9, Enabled: true}
		if err := tx.Skill.Save(ctx, record); err != nil {
			return err
		}
		c, _, err := tx.Character.Load(ctx, "31")
		if err != nil {
			return err
		}
		c.Stats["gold"] = 123
		if err := tx.Character.Save(ctx, c); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, _, _ := base.Settings.Load(ctx, DojoSkillScope("31"))
	actual, _, _ := base.Skill.Load(ctx, "31")
	c, _, _ := base.Character.Load(ctx, "31")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ordinary, actual) || c.Stats["gold"] == 123 {
		t.Fatal("道场配置或资产回滚失败")
	}
}
