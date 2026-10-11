package dnfbridge

import (
	"context"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFLearnableCapFields pins the evidence behind the "已精通"
// defect against the shipped archive: the learnable cap the client draws its
// "+"/"已精通" state from is `[growtype maximum level]`, which is strictly
// smaller than `[maximum level]` for these job-0 skills. A server that caps at
// `[maximum level]` can therefore persist (and re-send) a level the client
// renders as mastered with a dead "+" button.
//
// Opt-in because the archive is ~100 MB; the ordinary suite does not depend on
// it:
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE = "runtime\data\dnf\Script.pvf"
func TestRealScriptPVFLearnableCapFields(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf learnable-cap probe")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := parseInitialCharacterSkillsWithCatalog(archive, 0, catalog)
	if err != nil {
		t.Fatal(err)
	}
	// `character/Swordman/Swordman.chr` [initial value][skill] grants these six
	// rows at level > 0; 物品分解 starts at 7 and the rest at 1.
	initialLevels := map[uint16]int{}
	for _, entry := range initial {
		initialLevels[uint16(entry.SkillID)] = entry.Level
	}
	if initialLevels[179] != 7 || initialLevels[174] != 1 || initialLevels[46] != 1 {
		t.Fatalf("job0 [initial value][skill] = %+v", initialLevels)
	}

	tests := []struct {
		skillID      uint16
		name         string
		maximumLevel int
		growtypeCap  int
		growType     int
		characterLvl int
		wantLearn    int
	}{
		// 上挑: [maximum level]=20 but the client only lets a 剑魂 buy 10.
		{skillID: 46, name: "上挑", maximumLevel: 20, growtypeCap: 10, growType: 1, characterLvl: 90, wantLearn: 10},
		// 物理暴击: 20 vs 10.
		{skillID: 186, name: "物理暴击", maximumLevel: 20, growtypeCap: 10, growType: 1, characterLvl: 90, wantLearn: 10},
		// 基础精通: 100 vs 1 — the skill is always "已精通" on the client.
		{skillID: 174, name: "基础精通", maximumLevel: 100, growtypeCap: 1, growType: 1, characterLvl: 90, wantLearn: 1},
		// 物品分解: 10 vs 10, restored to its initial 7 by a reset.
		{skillID: 179, name: "物品分解", maximumLevel: 10, growtypeCap: 10, growType: 1, characterLvl: 90, wantLearn: 10},
	}
	for _, test := range tests {
		definition, ok := catalog.Find(0, test.skillID)
		if !ok {
			t.Fatalf("job0 skill %d is missing from the archive", test.skillID)
		}
		if definition.MaximumLevel != test.maximumLevel {
			t.Fatalf("%s [maximum level] = %d, want %d", test.name, definition.MaximumLevel, test.maximumLevel)
		}
		if got := definition.MaxLevelForGrowType(test.growType); got != test.growtypeCap {
			t.Fatalf("%s [growtype maximum level][%d] = %d, want %d", test.name, test.growType, got, test.growtypeCap)
		}
		if got := definition.MaxLearnableLevel(test.growType, test.characterLvl); got != test.wantLearn {
			t.Fatalf("%s learnable at level %d = %d, want %d", test.name, test.characterLvl, got, test.wantLearn)
		}
	}
}
