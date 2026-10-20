package dnfbridge

import (
	"os"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestUpgradeCostWeightsPreserveFractionalIndexes(t *testing.T) {
	raw := "[cost]\n100 200 300\n[cost weights by rarity]\n0.5 1 1.25 2 2.75 1.1 1.3\n[type]\n1 0.75 1.25 0 2.5\n"
	doc, err := dnfpvf.Parse(currentUpgradePVFPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	table := parseCurrentUpgradeTable(doc, raw)
	if want := []float64{0.5, 1, 1.25, 2, 2.75, 1.1, 1.3}; !reflect.DeepEqual(table.CostWeightsByRarity, want) {
		t.Errorf("rarity weights = %v, want %v", table.CostWeightsByRarity, want)
	}
	if want := []float64{1, 0.75, 1.25, 0, 2.5}; !reflect.DeepEqual(table.TypeWeights, want) {
		t.Errorf("type weights = %v, want %v", table.TypeWeights, want)
	}
	if want := []int{100, 200, 300}; !reflect.DeepEqual(table.Costs, want) {
		t.Errorf("costs = %v, want %v", table.Costs, want)
	}
}

func TestRealPVFUpgradeCostWeightsPreserveFractionalIndexes(t *testing.T) {
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
	if want := []float64{0.5, 1.5, 3, 5, 8, 4, 6}; !reflect.DeepEqual(table.CostWeightsByRarity, want) {
		t.Fatalf("rarity weights = %v, want %v", table.CostWeightsByRarity, want)
	}
	if want := []float64{1.2, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}; !reflect.DeepEqual(table.TypeWeights, want) {
		t.Fatalf("type weights = %v, want %v", table.TypeWeights, want)
	}
}
