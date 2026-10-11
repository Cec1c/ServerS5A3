package skill

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func capRepairTable(t *testing.T) *Table {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), textSource{
		"skill/skilllist.lst": "0 `job0.lst`\n",
		"skill/job0.lst": "46 `job0/upperslash.skl`\n" +
			"174 `job0/basicattackup.skl`\n" +
			"9 `job0/branchonly.skl`\n",
		"skill/job0/upperslash.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
20
[growtype maximum level]
10 10 10 10 10 10
[purchase cost]
20
[/purchase cost]
`,
		"skill/job0/basicattackup.skl": `
[type]
` + "`[passive]`" + `
[required level]
1
[maximum level]
100
[growtype maximum level]
1 1 1 1 1 1
[purchase cost]
0
[/purchase cost]
`,
		// An untransferred character may still own this row from the legacy
		// import; the repair must report it and never rewrite it.
		"skill/job0/branchonly.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
60
[growtype maximum level]
0 0 0 0 0 0
[purchase cost]
40
[/purchase cost]
`,
	}, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := Load(context.Background(), index, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func capRepairFixture(t *testing.T) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "7",
		Skills: map[int64]dnfrepo.SkillState{
			46:  {Level: 20, Enabled: true},
			174: {Level: 5, Enabled: true},
			9:   {Level: 3, Enabled: true},
		},
		Points: dnfrepo.SkillPointState{
			TotalSP: 1000, RemainingSP: 100,
			TotalTP: 10, RemainingTP: 10,
			SyncedLevel: 90,
		},
		Layouts: map[int]dnfrepo.SkillLayout{0: {0: 46, 1: 174, 2: 9}},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}
	return repos
}

// Reproduces the operator report "回退技能加点显示已精通该技能": a persisted level
// above the client's `[growtype maximum level]` cap is clamped back and the
// difference is refunded, so the skill becomes levelable again.
func TestRepairLevelCapsClampsOverCapRowsAndRefundsPoints(t *testing.T) {
	ctx := context.Background()
	repos := capRepairFixture(t)
	owner, err := NewSceneOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	record, result, err := owner.RepairLevelCaps(ctx, capRepairTable(t), LevelCapRepairCommand{
		CharacterID:    "7",
		Job:            0,
		GrowType:       1,
		CharacterLevel: 90,
		InitialLevels:  map[uint16]int{46: 1, 174: 1},
	})
	if err != nil {
		t.Fatalf("RepairLevelCaps() error = %v", err)
	}
	want := []LevelCapClamp{
		{SkillID: 46, From: 20, To: 10, MaxLearnable: 10},
		{SkillID: 174, From: 5, To: 1, MaxLearnable: 1},
	}
	if !reflect.DeepEqual(result.Clamps, want) {
		t.Fatalf("clamps = %+v want %+v", result.Clamps, want)
	}
	if !reflect.DeepEqual(result.Unavailable, []uint16{9}) {
		t.Fatalf("unavailable = %+v, want [9]", result.Unavailable)
	}
	// 46 refunds levels 10..19 at 20 SP each; 174's purchase cost is 0.
	if record.Points.RemainingSP != 300 {
		t.Fatalf("RemainingSP = %d, want 300", record.Points.RemainingSP)
	}
	if record.Skills[46].Level != 10 || record.Skills[174].Level != 1 {
		t.Fatalf("repaired skills = %+v", record.Skills)
	}
	if record.Skills[9].Level != 3 {
		t.Fatalf("direction-unavailable skill was rewritten: %+v", record.Skills[9])
	}

	saved, found, err := repos.Skill.Load(ctx, "7")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	if saved.Skills[46].Level != 10 || saved.Skills[174].Level != 1 || saved.Points.RemainingSP != 300 {
		t.Fatalf("persisted record = %+v", saved)
	}

	// Idempotent: a second pass changes nothing.
	_, again, err := owner.RepairLevelCaps(ctx, capRepairTable(t), LevelCapRepairCommand{
		CharacterID: "7", Job: 0, GrowType: 1, CharacterLevel: 90,
		InitialLevels: map[uint16]int{46: 1, 174: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Fatalf("second pass clamped again: %+v", again.Clamps)
	}
}

func TestSkillsAtLearnableCapNamesTheMasteredRows(t *testing.T) {
	table := capRepairTable(t)
	entries := SkillsAtLearnableCap(table, 0, 1, 90, map[int64]dnfrepo.SkillState{
		46:  {Level: 10, Enabled: true},
		174: {Level: 1, Enabled: true},
		9:   {Level: 3, Enabled: true},
	})
	want := []SkillAtCapEntry{
		{SkillID: 46, Level: 10, MaxLearnable: 10},
		{SkillID: 174, Level: 1, MaxLearnable: 1},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("at-cap = %+v want %+v", entries, want)
	}
}

func TestRepairLevelCapsRequiresCatalogAndCharacter(t *testing.T) {
	ctx := context.Background()
	owner, err := NewSceneOwner(capRepairFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := owner.RepairLevelCaps(ctx, nil, LevelCapRepairCommand{CharacterID: "7", Job: 0}); err == nil {
		t.Fatal("nil catalog was accepted")
	}
	if _, _, err := owner.RepairLevelCaps(ctx, capRepairTable(t), LevelCapRepairCommand{}); err == nil {
		t.Fatal("empty character id was accepted")
	}
}
