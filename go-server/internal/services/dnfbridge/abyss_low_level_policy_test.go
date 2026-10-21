package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func TestAbyssLowLevelRealFragments(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to an explicit PVF")
	}
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := currentEpicPieceCatalogFor(mc)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := loadHellPartyRewardEquipmentPool(archive)
	if err != nil {
		t.Fatal(err)
	}
	paths := zzRealEquipmentPaths(t, archive)
	for _, tc := range []struct {
		id     int64
		count  int
		levels map[int64]int
	}{{86, 462, map[int64]int{65: 67, 70: 200, 75: 195}}, {87, 395, map[int64]int{70: 200, 75: 195}}, {190, 248, map[int64]int{85: 138, 90: 110}}, {310, 248, map[int64]int{85: 138, 90: 110}}} {
		t.Run(fmt.Sprint(tc.id), func(t *testing.T) {
			d, ok := table.FindDungeon(tc.id)
			if !ok {
				t.Fatal(tc.id)
			}
			minimum, basis := d.Metadata.MinimumRequiredLevel.Value, d.Metadata.BasisLevel.Value
			band := abyssItemLevelBandFromEnv().forDungeon(minimum, basis)
			pieces, idx, err := currentAbyssEpicPieceLevelCandidates(mc, cat, band)
			if err != nil {
				t.Fatal(err)
			}
			if len(pieces) != tc.count {
				t.Fatalf("pieces=%d want=%d", len(pieces), tc.count)
			}
			counts := map[int64]int{}
			for _, piece := range pieces {
				output, ok := cat.outputFor(piece)
				if !ok {
					t.Fatal(piece)
				}
				def := zzRealEquipmentDefinition(t, archive, paths, int64(output))
				lv, known := idx.levelOf(piece)
				if !known || lv != def.minimumLevel || def.rarity != 4 {
					t.Fatalf("unresolved/mismatched output %d", output)
				}
				if tc.id == 86 || tc.id == 87 {
					if def.grade < minimum || def.grade > basis+7 {
						t.Fatalf("out of real grade band output=%d grade=%d", output, def.grade)
					}
				}
				counts[lv]++
			}
			if len(counts) != len(tc.levels) {
				t.Fatalf("levels=%v want=%v", counts, tc.levels)
			}
			for lv, count := range tc.levels {
				if counts[lv] != count {
					t.Fatalf("levels=%v want=%v", counts, tc.levels)
				}
			}
			// Both selection consumers and the rarity diagnostics must see the same pool.
			lo, hi, _ := hellPartyRewardGradeRangeForLevels(minimum, basis)
			countsByRarity := pool.rarityCandidateCounts(lo, hi, band)
			for seed := uint32(0); seed < 16; seed++ {
				a, err := pool.selectEquipment(newCurrentDungeonDropLCG(seed), minimum, basis, 4, false)
				if err != nil {
					t.Fatal(err)
				}
				b, err := pool.selectEquipmentWithinRarities(newCurrentDungeonDropLCG(seed), minimum, basis, []int64{4}, false)
				if err != nil {
					t.Fatal(err)
				}
				if !a.Selected || !b.Selected || a.CandidateCount != b.CandidateCount || a.CandidateCount != countsByRarity[hellPartyRewardRarityIndex(4)] {
					t.Fatalf("consumer mismatch: a=%+v b=%+v counts=%v", a, b, countsByRarity)
				}
				def := zzRealEquipmentDefinition(t, archive, paths, int64(b.ItemID))
				if def.grade < lo || def.grade > hi {
					t.Fatalf("selected grade=%d outside %d..%d", def.grade, lo, hi)
				}
			}
		})
	}
}

func TestAbyssLowLevelPolicyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, floor, ceiling string
		minimum, basis       int64
		grade                bool
	}{
		{"default_low", "", "", 71, 74, true}, {"boundary_low", "", "", 84, 84, true},
		{"high_minimum", "", "", 85, 84, false}, {"high_basis", "", "", 84, 85, false},
		{"unknown_minimum", "", "", 0, 74, false}, {"unknown_basis", "", "", 71, 0, false},
		{"explicit_floor", "85", "", 71, 74, false}, {"explicit_ceiling", "", "90", 71, 74, false},
		{"custom_band", "70", "75", 71, 74, false}, {"invalid", "junk", "", 71, 74, false},
		{"invalid_dash", "-", "", 71, 74, false}, {"legacy", "legacy", "", 71, 74, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := readAbyssItemLevelBand(tc.floor, tc.ceiling)
			scoped := original.forDungeon(tc.minimum, tc.basis)
			if (scoped.GradeMin > 0) != tc.grade {
				t.Fatalf("band=%+v", scoped)
			}
			if !tc.grade && scoped != original {
				t.Fatalf("explicit/high/unknown policy changed %+v -> %+v", original, scoped)
			}
			if tc.grade && scoped.permitsEquipmentLevel(0) {
				t.Fatal("unknown equipment admitted")
			}
		})
	}
	for _, tc := range []struct{ name, old, new string }{
		{"unknown_grade", "[grade]\n90\n", ""}, {"grade_zero", "[grade]\n90\n", "[grade]\n0\n"},
		{"wrong_rarity", "[rarity]\n4\n", "[rarity]\n1\n"}, {"unknown_level", "[minimum level]\n85\n", ""},
		{"high_grade", "[grade]\n90\n", "[grade]\n96\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := bridgeAbyssEpicFragmentPVF()
			for i := 0; i < 3; i++ {
				p := fmt.Sprintf("equipment/piece/%d.equ", 450100+i)
				source[p] = strings.Replace(source[p], tc.old, tc.new, 1)
				source[p] = strings.Replace(source[p], "[grade]\n90\n", "[grade]\n76\n", 1)
				source[p] = strings.Replace(source[p], "[minimum level]\n85\n", "[minimum level]\n70\n", 1)
			}
			mc, err := newPVFDungeonMonsterCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			cat, err := currentEpicPieceCatalogFor(mc)
			if err != nil {
				t.Fatal(err)
			}
			pieces, _, err := currentAbyssEpicPieceLevelCandidates(mc, cat, readAbyssItemLevelBand("", "").forDungeon(71, 74))
			if tc.name == "unknown_level" {
				if err == nil {
					t.Fatal("unresolved index accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(pieces) != 0 {
				t.Fatalf("invalid output admitted: %v", pieces)
			}
		})
	}
}
