package dnfbridge

import (
	"context"
	"errors"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFBerserkerCanLearnBurningBlood pins the real list entry and
// exercises the same skillcmd buy path as the game. The current PVF deliberately
// keeps [skill fitness growtype] = 1 while its maximum-level owner enables
// grow-type 3; A21 ignores that metadata and gates on the maximum-level table.
//
// Opt-in because the shipped archive is about 100 MB:
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE = "runtime\data\dnf\Script.pvf"
func TestRealScriptPVFBerserkerCanLearnBurningBlood(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf Burning Blood probe")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}

	listText, err := archive.ReadText("skill/SwordmanSkill.lst")
	if err != nil {
		t.Fatal(err)
	}
	listDocument, err := dnfpvf.Parse("skill/SwordmanSkill.lst", listText)
	if err != nil {
		t.Fatal(err)
	}
	const burningBloodPath = "skill/Swordman/Rejection.skl"
	var skillID int64
	for _, entry := range dnfpvf.ParseList(listDocument) {
		if strings.EqualFold(path.Clean("skill/"+entry.Path), burningBloodPath) {
			skillID = entry.ID
			break
		}
	}
	if skillID != 248 {
		t.Fatalf("real SwordmanSkill.lst maps %s to id %d, want 248", burningBloodPath, skillID)
	}
	definition, ok := catalog.Find(0, uint16(skillID))
	if !ok {
		t.Fatalf("job0 skill %d is missing from the catalog", skillID)
	}
	if definition.Name != "燃烧之血" || !strings.EqualFold(path.Clean(definition.Path), burningBloodPath) {
		t.Fatalf("real skill identity = id=%d name=%q path=%q", definition.ID, definition.Name, definition.Path)
	}
	if definition.RequiredLevel != 15 || definition.MaximumLevel != 1 || definition.FixedLevelSkill {
		t.Fatalf("real skill rules = required=%d max=%d fixed=%t", definition.RequiredLevel, definition.MaximumLevel, definition.FixedLevelSkill)
	}
	if !reflect.DeepEqual(definition.GrowtypeMaximumLevels, []int{0, 0, 0, 1, 0, 1}) ||
		!reflect.DeepEqual(definition.GrowTypes, []int{1}) {
		t.Fatalf("real skill tables = maximum=%v fitness=%v", definition.GrowtypeMaximumLevels, definition.GrowTypes)
	}
	if !definition.SupportsCharacterGrowth(3) || definition.SupportsCharacterGrowth(1) ||
		definition.MaxLearnableLevel(3, 15) != 1 {
		t.Fatalf("real skill direction rules = grow3=%t grow1=%t cap=%d",
			definition.SupportsCharacterGrowth(3), definition.SupportsCharacterGrowth(1), definition.MaxLearnableLevel(3, 15))
	}

	repos := dnfrepomemory.NewMemoryGroup()
	const characterID = "60248"
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterID,
		AccountID:   "burning-blood-account",
		Job:         "0",
		Level:       15,
		Stats:       map[string]int64{"grow_type": 3, "skill_tree_index": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: characterID,
		Skills:      map[int64]dnfrepo.SkillState{},
		Points: dnfrepo.SkillPointState{
			TotalSP: 100, RemainingSP: 100,
			TotalTP: 10, RemainingTP: 10,
			SyncedLevel: 15,
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := skillcmd.NewOwner(repos, skillcmd.OwnerOptions{
		Catalog:       catalog,
		InitialLevels: map[uint16]int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := skillcmd.NewBuyCommand(alignedcmd.Request{
		AccountID:           "burning-blood-account",
		SelectedCharacterID: 60248,
	}, skillcmd.BuySkillRequest{
		RawSkillTree: 0,
		Count:        1,
		Entries:      []skillcmd.BuySkillEntry{{SkillID: uint16(skillID), LevelDelta: 1}},
	})
	result, err := owner.ApplyBuy(ctx, command)
	if err != nil {
		t.Fatalf("Berserker learn 248 through skillcmd failed: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].SkillID != 248 || result.Entries[0].Level != 1 {
		t.Fatalf("learn result = %+v", result.Entries)
	}
	persisted, found, err := repos.Skill.Load(ctx, characterID)
	if err != nil || !found {
		t.Fatalf("reload learned skill found=%t err=%v", found, err)
	}
	if persisted.Skills[248].Level != 1 || !persisted.Skills[248].Enabled {
		t.Fatalf("persisted Burning Blood = %+v", persisted.Skills[248])
	}
	if result.Points.RemainingSP != persisted.Points.RemainingSP {
		t.Fatalf("zero-cost learn changed SP: result=%+v persisted=%+v", result.Points, persisted.Points)
	}

	// The PVF maximum is one. A second point is correctly mastered/rejected.
	if _, err := owner.ApplyBuy(ctx, command); !errors.Is(err, skillcmd.ErrSkillLevel) {
		t.Fatalf("second level beyond PVF maximum error = %v, want ErrSkillLevel", err)
	}
}
