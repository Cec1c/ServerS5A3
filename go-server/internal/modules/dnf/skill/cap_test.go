package skill

import (
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// The learnable cap the client draws its "+"/"已精通" state from is
// `[growtype maximum level]`, not `[maximum level]`. These are the real values
// of the shipped Script.pvf (`skill/swordman/*.skl`): 上挑 and 物理暴击 declare
// 20/10, 基础精通 declares 100/1.
func TestMaxLearnableLevelUsesGrowtypeTable(t *testing.T) {
	upperSlash := Skill{
		ID: 46, Job: 0, RequiredLevel: 1, MaximumLevel: 20,
		GrowtypeMaximumLevels: []int{10, 10, 10, 10, 10, 10},
	}
	if got := upperSlash.MaxLevelForGrowType(1); got != 10 {
		t.Fatalf("上挑 growtype cap = %d, want 10", got)
	}
	if got := upperSlash.MaxLearnableLevel(1, 90); got != 10 {
		t.Fatalf("上挑 level-90 learnable = %d, want 10 (not [maximum level]=20)", got)
	}
	if got := upperSlash.MaxLearnableLevel(1, 3); got != 3 {
		t.Fatalf("上挑 level-3 learnable = %d, want 3", got)
	}

	basicTraining := Skill{
		ID: 174, Job: 0, RequiredLevel: 1, MaximumLevel: 100,
		GrowtypeMaximumLevels: []int{1, 1, 1, 1, 1, 1},
	}
	if got := basicTraining.MaxLearnableLevel(0, 90); got != 1 {
		t.Fatalf("基础精通 learnable = %d, want 1 (not [maximum level]=100)", got)
	}

	branchOnly := Skill{
		ID: 9, Job: 0, RequiredLevel: 35, MaximumLevel: 60,
		GrowtypeMaximumLevels: []int{0, 50, 0, 0, 0, 50},
	}
	if got := branchOnly.MaxLearnableLevel(0, 90); got != 0 {
		t.Fatalf("拔刀斩 on the untransferred branch = %d, want 0", got)
	}
	if got := branchOnly.MaxLearnableLevel(1, 90); got != 50 {
		t.Fatalf("拔刀斩 on 剑魂 = %d, want 50", got)
	}
	if got := branchOnly.MaxLearnableLevel(1, 40); got != 6 {
		t.Fatalf("拔刀斩 at level 40 = %d, want 6 (required 35 + one per level)", got)
	}
}

func TestMaxLearnableLevelHonoursSecondGrowtypeTable(t *testing.T) {
	trial := Skill{
		ID: 246, Job: 0, RequiredLevel: 1, MaximumLevel: 9,
		GrowtypeMaximumLevels:       []int{0, 1, 0, 0, 0, 1},
		SecondGrowtypeMaximumLevels: []int{5, 0, 3, 4, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	// 神影手's real table is 12 slots indexed by branch*2 + (awakening stage - 1)
	// (86JP SkillDataProvider.GetConfiguredMaxLevelFor). The persisted grow_type
	// packs the branch in the LOW nibble and the awakening stage in the HIGH
	// nibble, so:
	//   0x11 = 剑魂 (branch 1) / stage 1 -> slot 2 -> 3
	//   0x21 = 剑魂 / stage 2            -> slot 3 -> 4
	//   0x10 = 未转职 / stage 1           -> slot 0 -> 5
	//   0x12 = branch 2 / stage 1         -> slot 4 -> 0, falls back to the
	//          first table's branch-2 slot, which is 0 (unlearnable).
	tests := []struct {
		packed int
		want   int
	}{
		{packed: 0x11, want: 3},
		{packed: 0x21, want: 4},
		{packed: 0x10, want: 5},
		{packed: 0x12, want: 0},
	}
	for _, test := range tests {
		if got := trial.MaxLevelForGrowType(test.packed); got != test.want {
			t.Fatalf("grow_type=%#x cap = %d, want %d", test.packed, got, test.want)
		}
	}
}

func TestMaxLearnableLevelFallsBackToMaximumLevelWithoutGrowtypeTable(t *testing.T) {
	plain := Skill{ID: 1, Job: 0, RequiredLevel: 1, MaximumLevel: 15}
	if got := plain.MaxLearnableLevel(0, 90); got != 15 {
		t.Fatalf("plain learnable = %d, want 15", got)
	}
	if got := plain.MaxLearnableLevel(0, 1); got != 1 {
		t.Fatalf("plain learnable at level 1 = %d, want 1", got)
	}
}

func TestParseReadsGrowtypeMaximumLevelAndLevelInterval(t *testing.T) {
	text := `
[type]
` + "`[active]`" + `
[required level]
10
[maximum level]
20
[growtype maximum level]
4 8 12 16 20 24
[second growtype maximum level]
1 2 3
[level interval]
2
[purchase cost]
15
[/purchase cost]
`
	document, err := parseTestDocument(text)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := parseSkill(0, 77, "skill/test.skl", document)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.GrowtypeMaximumLevels) != 6 || definition.GrowtypeMaximumLevels[3] != 16 {
		t.Fatalf("growtype maximum level = %v", definition.GrowtypeMaximumLevels)
	}
	if len(definition.SecondGrowtypeMaximumLevels) != 3 || definition.SecondGrowtypeMaximumLevels[2] != 3 {
		t.Fatalf("second growtype maximum level = %v", definition.SecondGrowtypeMaximumLevels)
	}
	if definition.LevelInterval != 2 {
		t.Fatalf("level interval = %d, want 2", definition.LevelInterval)
	}
	// required 10, interval 2 on 剑魂 (table slot 1 = 8) -> min(8, 20, 1+(30-10)/2)=8
	if got := definition.MaxLearnableLevel(1, 30); got != 8 {
		t.Fatalf("learnable = %d, want 8", got)
	}
	// Below the required level the direction is closed.
	if got := definition.MaxLearnableLevel(1, 5); got != 0 {
		t.Fatalf("learnable below required level = %d, want 0", got)
	}
}

func parseTestDocument(text string) (*dnfpvf.Document, error) {
	return dnfpvf.Parse("skill/test.skl", text)
}
