package pvp

import (
	"context"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func TestPVPTrainingChoiceCopyOnlyActivePageAndIgnoreReplay(t *testing.T) {
	for _, choice := range []byte{1, 2} {
		t.Run(string(rune('0'+choice)), func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			c := repo.CharacterRecord{CharacterID: "31", AccountID: "choice", Level: 10, Stats: map[string]int64{}}
			if err := r.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			ordinary := repo.SkillRecord{CharacterID: "31", Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}},
				Points:  repo.SkillPointState{TotalSP: 100, RemainingSP: 80, SyncedLevel: 10},
				Trees:   repo.SkillTreeState{Expanded: true, Active: 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 2, Enabled: true}}, Points: repo.SkillPointState{TotalSP: 100, RemainingSP: 70}}},
				Layouts: map[int]repo.SkillLayout{0: {198: 46}, 1: {199: 46}}}
			if err := r.Skill.Save(ctx, ordinary); err != nil {
				t.Fatal(err)
			}
			pk, err := skillcmd.PreparePVPSkills(ctx, r, c.AccountID, c.CharacterID)
			if err != nil {
				t.Fatal(err)
			}
			pk.Skills[46] = repo.SkillState{Level: 4, Enabled: true}
			pk.Points.RemainingSP = 40
			pk.Layouts[0] = repo.SkillLayout{200: 46}
			if err := skillcmd.PVPRepositories(r, c.AccountID, c.CharacterID).CharacterSkills.WithinCharacterSkill(ctx, c.CharacterID, func(store repo.SkillRepository) error { return store.Save(ctx, pk) }); err != nil {
				t.Fatal(err)
			}
			if err := ApplyDojoSkillChoice(ctx, r, c.AccountID, c.CharacterID, choice, time.Now()); err != nil {
				t.Fatal(err)
			}
			after, _, _ := r.Skill.Load(ctx, c.CharacterID)
			actualPK, err := skillcmd.PreparePVPSkills(ctx, r, c.AccountID, c.CharacterID)
			if err != nil || !reflect.DeepEqual(actualPK.Skills, pk.Skills) || !reflect.DeepEqual(actualPK.Layouts, pk.Layouts) {
				t.Fatal("复制选择改写了PK存档", err)
			}
			if choice == 2 {
				if !reflect.DeepEqual(after, ordinary) {
					t.Fatal("关闭复制弹窗改变刷图加点")
				}
				return
			}
			if after.Skills[46].Level != 4 || after.Points.RemainingSP != 40 || after.Layouts[1][200] != 46 || repo.ActiveSkillTree(after) != 1 {
				t.Fatal("未复制到普通当前页")
			}
			if !reflect.DeepEqual(after.Trees.Other, ordinary.Trees.Other) || !reflect.DeepEqual(after.Layouts[0], ordinary.Layouts[0]) {
				t.Fatal("覆盖了普通另一页")
			}
			after.Skills[46] = repo.SkillState{Level: 3, Enabled: true}
			if err := r.Skill.Save(ctx, after); err != nil {
				t.Fatal(err)
			}
			if err := ApplyDojoSkillChoice(ctx, r, c.AccountID, c.CharacterID, 1, time.Now()); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := r.Skill.Load(ctx, c.CharacterID)
			if replayed.Skills[46].Level != 3 {
				t.Fatal("重放复制覆盖了后续刷图修改")
			}
		})
	}
}
