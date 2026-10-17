package skillcmd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func unlockSkillOwnerFixture(t *testing.T, ctx context.Context, repos repo.Group) {
	t.Helper()
	record, _, err := repos.Skill.Load(ctx, "31")
	if err != nil {
		t.Fatal(err)
	}
	record.Trees = repo.SkillTreeState{Expanded: true, Other: repo.SkillProfile{
		Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}, Points: record.Points,
	}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func TestSkillTreeExpansionPreservesFirstAndBuildsFreeSecond(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	before, _, _ := repos.Skill.Load(ctx, "31")
	character, _, _ := repos.Character.Load(ctx, "31")
	before.Skills[46] = repo.SkillState{Level: 3, Enabled: true}
	before.Points.RemainingSP = 60
	before.Layouts = map[int]repo.SkillLayout{0: {4: 46}}
	before.Cooldowns = map[int64]time.Time{46: time.Unix(1234, 0)}
	next, err := ExpandSkillTree(character, before, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}, PointBaseline: &before.Points})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Trees.Expanded || next.Trees.Active != 0 || next.Trees.Other.Skills[46].Level != 1 || next.Trees.Other.Points.RemainingSP != 100 ||
		!reflect.DeepEqual(next.Skills, before.Skills) || next.Points != before.Points || next.Layouts[0][4] != 46 || !reflect.DeepEqual(next.Cooldowns, before.Cooldowns) {
		t.Fatalf("扩展结果: %+v", next)
	}
	next.Trees.Other.Skills[46] = repo.SkillState{Level: 9}
	if before.Skills[46].Level != 3 {
		t.Fatal("两页共用了技能映射")
	}
	if _, err := ExpandSkillTree(character, next, OwnerOptions{}); !errors.Is(err, ErrSkillTreeAlreadyExpanded) {
		t.Fatalf("重复购买: %v", err)
	}
}

func TestSkillTreesSwitchLearnSlotResetAndRelogStayIndependent(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	unlockSkillOwnerFixture(t, ctx, repos)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Layouts = map[int]repo.SkillLayout{0: {4: 46}, 1: {0: 46}}
	record.Cooldowns = map[int64]time.Time{46: time.Unix(1234, 0)}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	options := OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}, PointBaseline: &record.Points}
	owner, _ := NewOwner(repos, options)
	switchPage := func(current byte) {
		t.Helper()
		if _, err := owner.ApplyTreeSwitch(ctx, NewTreeCommand(alignedRequest("acc-skill", 31), ChangeAnotherSkillTreeRequest{SkillTree: current})); err != nil {
			t.Fatal(err)
		}
	}
	switchPage(0)
	_, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 1, Count: 2, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}, {SkillID: 48, LevelDelta: 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{SkillTree: 1, From: 0, To: 5, ContextIndex: -1})); err != nil {
		t.Fatal(err)
	}
	selected, _, _ := repos.Skill.Load(ctx, "31")
	if selected.Skills[46].Level != 2 || selected.Points.RemainingSP != 80 || selected.Points.RemainingTP != 8 || selected.Trees.Other.Skills[46].Level != 1 || selected.Trees.Other.Points.RemainingSP != 100 || selected.Layouts[1][5] != 46 || selected.Layouts[0][4] != 46 {
		t.Fatalf("第二页修改泄漏: %+v", selected)
	}
	// 新owner模拟重登，不借用旧请求对象中的页码或技能缓存。
	owner, _ = NewOwner(repos, options)
	switchPage(1)
	selected, _, _ = repos.Skill.Load(ctx, "31")
	if selected.Skills[46].Level != 1 || selected.Points.RemainingSP != 100 || selected.Points.RemainingTP != 10 || selected.Trees.Other.Skills[46].Level != 2 {
		t.Fatalf("返回第一页: %+v", selected)
	}
	if _, err := owner.ApplyReset(ctx, NewResetCommand(alignedRequest("acc-skill", 31), SkillInitRequest{SkillTree: 0})); err != nil {
		t.Fatal(err)
	}
	switchPage(0)
	selected, _, _ = repos.Skill.Load(ctx, "31")
	if selected.Skills[46].Level != 2 || selected.Skills[48].Level != 1 || selected.Points.RemainingSP != 80 || selected.Points.RemainingTP != 8 || selected.Layouts[1][5] != 46 || !selected.Cooldowns[46].Equal(record.Cooldowns[46]) {
		t.Fatalf("重置或切换破坏另一页/冷却: %+v", selected)
	}
	if _, err := owner.ApplyTreeSwitch(ctx, NewTreeCommand(alignedRequest("acc-skill", 31), ChangeAnotherSkillTreeRequest{SkillTree: 0})); !errors.Is(err, ErrSkillTreeStateMismatch) {
		t.Fatalf("旧请求重放: %v", err)
	}
	if _, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{RawSkillTree: 0, Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}}})); !errors.Is(err, ErrSkillTreeStateMismatch) {
		t.Fatalf("修改非当前页: %v", err)
	}
}

func TestSkillTreeLockedHistoricalIndexCannotGrantOwnership(t *testing.T) {
	ctx := context.Background()
	for _, oldIndex := range []int64{-1, 0, 1, 255} {
		t.Run(fmt.Sprintf("历史页码=%d", oldIndex), func(t *testing.T) {
			repos := seededSkillOwnerRepositories(t, ctx)
			character, _, _ := repos.Character.Load(ctx, "31")
			character.Stats["skill_tree_index"] = oldIndex
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Skill.Load(ctx, "31")
			owner, _ := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
			if _, err := owner.ApplyTreeSwitch(ctx, Command{AccountID: "acc-skill", SelectedCharacterID: 31, SkillTree: 0}); !errors.Is(err, ErrSkillTreeUnavailable) {
				t.Fatalf("历史默认页码被当成购买凭据: %v", err)
			}
			if _, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedcmd.Request{AccountID: "acc-skill", SelectedCharacterID: 31}, BuySkillRequest{RawSkillTree: 1, Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}}})); !errors.Is(err, ErrSkillTreeUnavailable) {
				t.Fatalf("锁定页学习: %v", err)
			}
			after, _, _ := repos.Skill.Load(ctx, "31")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝路径改了原技能")
			}
		})
	}
}

func TestSkillTreeLevelGainPreservesIndependentSpentPoints(t *testing.T) {
	record := repo.SkillRecord{Points: repo.SkillPointState{TotalSP: 200, RemainingSP: 170, TotalTP: 20, RemainingTP: 18, SyncedLevel: 20}, Trees: repo.SkillTreeState{Expanded: true, Other: repo.SkillProfile{Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 6, SyncedLevel: 10}}}}
	changed, err := SyncOtherSkillPoints(&record)
	if err != nil || !changed || record.Trees.Other.Points.RemainingSP != 120 || record.Trees.Other.Points.RemainingTP != 16 {
		t.Fatalf("升级后另一页点数: %+v %v", record, err)
	}
}
