package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

func skillCommandProjectionCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	catalog, err := buildSkillCatalogFromSource(context.Background(), initialEquipmentMemSource{
		"skill/skilllist.lst": "0 `job0.lst`\n",
		"skill/job0.lst":      "46 `job0/upperslash.skl`\n47 `job0/branchonly.skl`\n",
		"skill/job0/upperslash.skl": "[type]\n`[active]`\n[required level]\n1\n[maximum level]\n20\n" +
			"[growtype maximum level]\n10 10 10 10 10 10\n[skill fitness growtype]\n0 1 2 3 4 5\n[/skill fitness growtype]\n[purchase cost]\n20\n[/purchase cost]\n",
		"skill/job0/branchonly.skl": "[type]\n`[active]`\n[required level]\n15\n[maximum level]\n60\n" +
			"[growtype maximum level]\n0 50 0 0 0 50\n[skill fitness growtype]\n1\n[/skill fitness growtype]\n[purchase cost]\n30\n[/purchase cost]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// The persisted op331 vector must come back on the wire as SKILLINFO field 4,
// otherwise the client's custom command is dropped at every login.
func TestBuildCurrentSceneSkillInfoBodyEmitsPersistedCustomCommands(t *testing.T) {
	record := dnfrepo.SkillRecord{
		Skills: map[int64]dnfrepo.SkillState{
			46: {Level: 1, Enabled: true},
			47: {Level: 2, Enabled: true},
		},
		Points: dnfrepo.SkillPointState{RemainingSP: 12, RemainingTP: 3},
	}
	layout := dnfrepo.SkillLayout{0: 46, 1: 47}

	without, _, err := buildCurrentSceneSkillInfoBody(record, layout, nil)
	if err != nil {
		t.Fatal(err)
	}
	commands := map[uint16][]byte{47: {0x03, 0x04}}
	with, _, err := buildCurrentSceneSkillInfoBody(record, layout, commands)
	if err != nil {
		t.Fatal(err)
	}
	if string(with) == string(without) {
		t.Fatal("persisted command vector did not change the skill info body")
	}

	_, outerMessages := consumeCurrentSkillInfoFields(t, with[4:])
	if len(outerMessages[2]) != currentSkillInfoTreeCount {
		t.Fatalf("tree count = %d", len(outerMessages[2]))
	}
	_, treeMessages := consumeCurrentSkillInfoFields(t, outerMessages[2][0])
	found := map[int][]byte{}
	for _, raw := range treeMessages[3] {
		fields, nested := consumeCurrentSkillInfoFields(t, raw)
		if len(fields[1]) != 1 || len(fields[2]) != 1 {
			t.Fatalf("invalid skill row fields=%v", fields)
		}
		slot := int(fields[1][0])
		if len(nested[4]) == 1 {
			found[slot] = append([]byte(nil), nested[4][0]...)
		}
	}
	if !reflect.DeepEqual(found, map[int][]byte{1: {0x03, 0x04}}) {
		t.Fatalf("field-4 rows = %+v", found)
	}
}

func TestLoadSkillCommandProjectionReadsPersistedSettings(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	service := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	if projection := service.loadSkillCommandProjection(ctx, repos, "9"); projection != nil {
		t.Fatalf("empty store projection = %+v", projection)
	}
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: skillcmd.SkillCommandScope("9"),
		Values: map[string]string{
			"command_46":    "0304",
			"command_count": "1",
			"source":        "current_exe_op331_skill_command_customizing",
		},
	}); err != nil {
		t.Fatal(err)
	}
	projection := service.loadSkillCommandProjection(ctx, repos, "9")
	if len(projection) != 1 || string(projection[46]) != string([]byte{0x03, 0x04}) {
		t.Fatalf("projection = %+v", projection)
	}
}

// The login repair must clamp an over-cap row and report the at-cap rows that
// the client renders as 已精通.
func TestRepairCurrentSceneSkillLevelCapsClampsAndReportsAtCap(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "9",
		AccountID:   "account-1",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 1},
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.SkillRecord{
		CharacterID: "9",
		Skills: map[int64]dnfrepo.SkillState{
			46: {Level: 20, Enabled: true}, // [maximum level]=20 but 剑魂 cap is 10
			47: {Level: 3, Enabled: true},  // 剑魂 cap 50, no clamp
		},
		Points: dnfrepo.SkillPointState{
			TotalSP: 1000, RemainingSP: 400,
			TotalTP: 10, RemainingTP: 10,
			SyncedLevel: 90,
		},
		Layouts: map[int]dnfrepo.SkillLayout{0: {0: 46, 1: 47}},
	}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		skillCatalog:       skillCommandProjectionCatalog(t),
		initialSkillsByJob: map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}},
	}
	repaired, err := service.repairCurrentSceneSkillLevelCaps(ctx, repos, character, record)
	if err != nil {
		t.Fatalf("repair error = %v", err)
	}
	if repaired.Skills[46].Level != 10 {
		t.Fatalf("skill 46 = %d, want 10", repaired.Skills[46].Level)
	}
	// 10 refunded levels at 20 SP each.
	if repaired.Points.RemainingSP != 600 {
		t.Fatalf("RemainingSP = %d, want 600", repaired.Points.RemainingSP)
	}
	saved, found, err := repos.Skill.Load(ctx, "9")
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	if saved.Skills[46].Level != 10 || saved.Points.RemainingSP != 600 {
		t.Fatalf("persisted = %+v", saved)
	}
}

// A character with no catalog (PVF unavailable) must keep the projection
// working instead of failing the login.
func TestRepairCurrentSceneSkillLevelCapsIsFailOpen(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "9", Job: "0", Level: 90}
	record := dnfrepo.SkillRecord{
		CharacterID: "9",
		Skills:      map[int64]dnfrepo.SkillState{46: {Level: 20, Enabled: true}},
	}
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	got, err := service.repairCurrentSceneSkillLevelCaps(ctx, repos, character, record)
	if err != nil {
		t.Fatalf("repair error = %v", err)
	}
	if got.Skills[46].Level != 20 {
		t.Fatalf("skill mutated without a catalog: %+v", got.Skills)
	}
}
