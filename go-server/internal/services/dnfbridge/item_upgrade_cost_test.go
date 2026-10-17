package dnfbridge

import (
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNativeUpgradeGoldCostRoundsAtEachStage(t *testing.T) {
	table := &currentUpgradeTable{Costs: []int{5}, TypeWeights: []float64{1.5}, CostWeightsByRarity: []float64{1.5}, CostWeightsByUpgradeLevel: map[int][2]float64{0: {0.5, 1.5}, 18: {2, 3}}}
	// 5 -> 8 -> 12 -> 6. A single rounding would yield 6, but using the
	// nonweapon column (kind 24 aliases weapon type, not level weight) is 18.
	for _, tc := range []struct {
		kind, level int
		want        int64
	}{{12, 0, 6}, {24, 0, 18}, {12, 19, 24}} {
		got, err := nativeUpgradeGoldCost(table, 0, 0, tc.kind, tc.level)
		if err != nil || got != tc.want {
			t.Fatalf("kind=%d level=%d got=%d err=%v want=%d", tc.kind, tc.level, got, err, tc.want)
		}
	}
	table.CostWeightsByUpgradeLevel[0] = [2]float64{1.5, 1.5}
	if got, err := nativeUpgradeGoldCost(table, 0, 0, 12, 0); err != nil || got != 18 {
		t.Fatalf("three rounded stages = %d, %v; want 18 (single rounding is 17)", got, err)
	}
	for _, tc := range [][4]int{{1, 0, 12, 0}, {0, 1, 12, 0}, {0, 0, 11, 0}, {0, 0, 26, 0}, {0, 0, 12, 1}, {0, 0, 12, -1}} {
		if _, err := nativeUpgradeGoldCost(table, tc[0], tc[1], tc[2], tc[3]); err == nil {
			t.Fatalf("accepted invalid metadata %v", tc)
		}
	}
}

func TestUpgradeLevelCostWeightsRejectMalformedRows(t *testing.T) {
	for _, raw := range []string{"0 1", "0 1 1 0 2 2", "0.5 1 1", "0 -1 1"} {
		doc, err := dnfpvf.Parse(currentUpgradePVFPath, "[cost weight by upgrade level]\n"+raw+"\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := parseCurrentUpgradeTable(doc, raw).CostWeightsByUpgradeLevel; len(got) != 0 {
			t.Fatalf("accepted %q: %v", raw, got)
		}
	}
}

func TestRealPVFNativeUpgradeGoldCost(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.ReadText(currentUpgradePVFPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(currentUpgradePVFPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	table := parseCurrentUpgradeTable(doc, raw)
	if len(table.CostWeightsByUpgradeLevel) != 19 || table.CostWeightsByUpgradeLevel[14] != [2]float64{8.5, 8.5} {
		t.Fatalf("level weights = %v", table.CostWeightsByUpgradeLevel)
	}
	// Native instruction stages for level-90 epic weapon, current +0:
	// 55395 -> 66474 -> 531792 -> 265896. This is a calculator test,
	// not an assertion that any live character was charged this amount.
	got, err := nativeUpgradeGoldCost(table, 90, 4, 12, 0)
	if err != nil || got != 265896 {
		t.Fatalf("cost=%d err=%v", got, err)
	}
}
