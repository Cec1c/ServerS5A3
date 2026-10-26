package skill

import (
	"context"
	"reflect"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func capRepairAutomaticTable(t *testing.T, unavailable bool) *Table {
	t.Helper()
	maximums := "1 1 1 1 1 1"
	if unavailable {
		maximums = "0 0 0 0 0 0"
	}
	index, err := dnfpvf.Build(context.Background(), textSource{
		DefaultList: "0 `job0.lst`\n",
		"skill/job0.lst": "174 `job0/automatic.skl`\n" +
			"179 `job0/free.skl`\n46 `job0/manual.skl`\n",
		"skill/job0/automatic.skl": "[required level]\n1\n[maximum level]\n100\n" +
			"[growtype maximum level]\n" + maximums + "\n" +
			"[fixed level skill]\n[interval level]\n1\n[add level per interval]\n1\n[/fixed level skill]\n" +
			"[purchase cost]\n0\n[/purchase cost]\n",
		"skill/job0/free.skl": "[required level]\n1\n[maximum level]\n100\n" +
			"[growtype maximum level]\n1 1 1 1 1 1\n[purchase cost]\n0\n[/purchase cost]\n",
		"skill/job0/manual.skl": "[required level]\n1\n[maximum level]\n20\n" +
			"[growtype maximum level]\n10 10 10 10 10 10\n[purchase cost]\n20\n[/purchase cost]\n",
	}, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := Load(context.Background(), index, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := table.Find(0, 174)
	if !ok || !definition.FixedLevelSkill || definition.FixedLevelForCharacter(25) != 25 {
		t.Fatal("自动成长夹具未按真实PVF容器解析", definition, ok)
	}
	if !unavailable && definition.MaxLearnableLevel(1, 25) != 1 {
		t.Fatal("自动成长基线不应改变手动可学上限", definition)
	}
	if definition.LevelCost(24) != 0 {
		t.Fatal("零成本自动成长夹具错误", definition)
	}
	return table
}

func TestRepairLevelCapsPreservesPVFFreeBaselines(t *testing.T) {
	for _, tc := range []struct {
		name         string
		automatic    int
		initial      int
		manual       int
		unavailable  bool
		observe      bool
		wantClamps   []LevelCapClamp
		wantExcluded []uint16
	}{
		{name: "合法自动成长等级", automatic: 25, initial: 7, manual: 10},
		{name: "不主动补高缺失等级", automatic: 20, initial: 4, manual: 10},
		{name: "自动等级超限", automatic: 30, initial: 7, manual: 10,
			wantClamps: []LevelCapClamp{{SkillID: 174, From: 30, To: 25, MaxLearnable: 1}}},
		{name: "初始免费等级超限", automatic: 25, initial: 9, manual: 10,
			wantClamps: []LevelCapClamp{{SkillID: 179, From: 9, To: 7, MaxLearnable: 1}}},
		{name: "手动技能仍退款", automatic: 25, initial: 7, manual: 20,
			wantClamps: []LevelCapClamp{{SkillID: 46, From: 20, To: 10, MaxLearnable: 10}}},
		{name: "不可用方向不改写", automatic: 30, initial: 7, manual: 10, unavailable: true,
			wantExcluded: []uint16{174}},
		{name: "观察档只报告", automatic: 30, initial: 7, manual: 10, observe: true,
			wantClamps: []LevelCapClamp{{SkillID: 174, From: 30, To: 25, MaxLearnable: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			table := capRepairAutomaticTable(t, tc.unavailable)
			repos := dnfrepomemory.NewMemoryGroup()
			before := dnfrepo.SkillRecord{
				CharacterID: "7",
				Skills: map[int64]dnfrepo.SkillState{
					174: {Level: tc.automatic, Enabled: true},
					179: {Level: tc.initial, Enabled: true},
					46:  {Level: tc.manual, Enabled: true},
				},
				Points:    dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: 25},
				Layouts:   map[int]dnfrepo.SkillLayout{0: {0: 174, 1: 179, 2: 46}},
				Cooldowns: map[int64]time.Time{46: time.Unix(100, 0).UTC()},
				UpdatedAt: time.Unix(1, 0).UTC(),
			}
			if err := repos.Skill.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			owner, err := NewSceneOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			command := LevelCapRepairCommand{
				CharacterID: "7", Job: 0, GrowType: 1, CharacterLevel: 25,
				InitialLevels: map[uint16]int{174: 1, 179: 7, 46: 1},
				UpdatedAt:     time.Unix(2, 0).UTC(), ObserveOnly: tc.observe,
			}
			record, result, err := owner.RepairLevelCaps(ctx, table, command)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Clamps, tc.wantClamps) || !reflect.DeepEqual(result.Unavailable, tc.wantExcluded) {
				t.Fatalf("修复或方向保护不符：result=%+v clamps=%+v unavailable=%v", result, tc.wantClamps, tc.wantExcluded)
			}
			want := dnfrepo.CloneSkill(before)
			if !tc.observe && len(tc.wantClamps) > 0 {
				for _, clamp := range tc.wantClamps {
					want.Skills[int64(clamp.SkillID)] = dnfrepo.SkillState{Level: clamp.To, Enabled: true}
					if clamp.SkillID == 46 {
						want.Points.RemainingSP += 200
					}
				}
				want.UpdatedAt = command.UpdatedAt
			}
			saved, found, err := repos.Skill.Load(ctx, "7")
			if err != nil || !found || !reflect.DeepEqual(record, want) || !reflect.DeepEqual(saved, want) {
				t.Fatalf("免费基线、账本或其它字段被误改：record=%+v saved=%+v want=%+v found=%t error=%v", record, saved, want, found, err)
			}
			if !tc.observe {
				command.UpdatedAt = time.Unix(3, 0).UTC()
				again, repeated, err := owner.RepairLevelCaps(ctx, table, command)
				if err != nil || repeated.Changed() || !reflect.DeepEqual(again, saved) {
					t.Fatalf("第二次修复不应重复保存或调整等级：result=%+v record=%+v error=%v", repeated, again, err)
				}
			}
		})
	}
}
