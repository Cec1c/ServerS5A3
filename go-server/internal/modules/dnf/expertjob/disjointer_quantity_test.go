package expertjob

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

const (
	disjointerQuantityEquipmentID     int64 = 5000
	disjointerQuantityCubeItemID      int64 = 5001
	disjointerQuantityNormalItemID    int64 = 5002
	disjointerQuantityBigWinItemID    int64 = 5003
	disjointerQuantityEquipmentRarity       = 6
	disjointerQuantityEquipmentGrade        = 90
)

type disjointerQuantityTestSource map[string]string

func (s disjointerQuantityTestSource) ReadText(relativePath string) (string, error) {
	text, ok := s[relativePath]
	if !ok {
		return "", fmt.Errorf("missing test PVF fixture %s", relativePath)
	}
	return text, nil
}

// disjointerQuantityCatalog keeps the test independent of production PVF data
// while exercising the same item-document and price-table parsing paths as Load.
func disjointerQuantityCatalog(t *testing.T, documents map[int64]string) *Catalog {
	t.Helper()
	source := disjointerQuantityTestSource{
		"equipment/pricetable.tbl": "[rate] 200 150 30\n",
	}
	items := make(map[int64]itemReference, len(documents))
	for itemID, body := range documents {
		itemPath := fmt.Sprintf("equipment/quantity-%d.equ", itemID)
		source[itemPath] = body
		items[itemID] = itemReference{kind: itemEquipment, path: itemPath}
	}
	catalog := &Catalog{source: source, items: items}
	if err := catalog.loadEquipmentSellRate(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func disjointerQuantityMaterialCount(materials []RecipeEntry, itemID int64) int64 {
	var count int64
	for _, material := range materials {
		if material.ItemID == itemID {
			count += material.Count
		}
	}
	return count
}

func TestEquipmentSellGoldAndPlanDisjointerMachineQuantity(t *testing.T) {
	catalog := disjointerQuantityCatalog(t, map[int64]string{
		disjointerQuantityEquipmentID: "[rarity] 6\n[minimum level] 90\n[grade] 90\n[value] 68400\n",
	})
	equipment, err := catalog.Equipment(disjointerQuantityEquipmentID)
	if err != nil {
		t.Fatal(err)
	}
	if equipment.SellGold != 13680 {
		t.Fatalf("equipment=%+v: sell gold = %d, want 13680", equipment, equipment.SellGold)
	}

	for _, tc := range []struct {
		name       string
		machine    int
		multiplier float64
		want       int64
	}{
		{name: "machine 1 rarity 6", machine: 1, multiplier: 1.08, want: 108},
		{name: "machine 11 rarity 6", machine: 11, multiplier: 1.62, want: 162},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog.disjointer = &DisjointerConfig{
				BaseConst: 150,
				Results: map[disjointerResultKey]DisjointerResultRule{
					{MachineGrade: tc.machine - 1, Rarity: disjointerQuantityEquipmentRarity, State: 0}: {
						ItemID:     disjointerQuantityCubeItemID,
						Multiplier: tc.multiplier,
					},
				},
			}
			plan, err := catalog.PlanDisjointer(0, tc.machine, equipment, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := disjointerQuantityMaterialCount(plan.Materials, disjointerQuantityCubeItemID); got != tc.want {
				t.Fatalf("clear cube quantity = %d, want %d; plan=%+v", got, tc.want, plan)
			}
		})
	}
}

func TestPlanDisjointerOnlySuppressesCriticalForWarriorsDungeon(t *testing.T) {
	base := "[rarity] 6\n[minimum level] 90\n[grade] 90\n[value] 68400\n"
	catalog := disjointerQuantityCatalog(t, map[int64]string{
		5100: base + "[custom ability type] `warriors dungeon`\n",
		5101: base,
		5102: base + "[custom ability type] `epic production`\n",
	})
	catalog.disjointer = &DisjointerConfig{
		BaseConst: 150,
		Results: map[disjointerResultKey]DisjointerResultRule{
			{MachineGrade: 0, Rarity: disjointerQuantityEquipmentRarity, State: 0}: {
				ItemID:              disjointerQuantityCubeItemID,
				Multiplier:          1.08,
				AdditionalTable:     1,
				BigWinTable:         2,
				BigWinChancePercent: 100,
			},
		},
		AdditionalResults: map[int][]DisjointerSelectionRule{
			1: {{MinimumGrade: 0, MaximumGrade: disjointerQuantityEquipmentGrade, ItemID: disjointerQuantityNormalItemID, Weight: 1, CountDivisor: 5.70}},
		},
		BigWinResults: map[int][]DisjointerSelectionRule{
			2: {{MinimumGrade: 0, MaximumGrade: disjointerQuantityEquipmentGrade, ItemID: disjointerQuantityBigWinItemID, Weight: 1, CountDivisor: 0.19}},
		},
	}

	for _, tc := range []struct {
		name         string
		itemID       int64
		warriors     bool
		materialID   int64
		wantQuantity int64
	}{
		{name: "warriors dungeon uses normal row", itemID: 5100, warriors: true, materialID: disjointerQuantityNormalItemID, wantQuantity: 15},
		{name: "ordinary equipment keeps critical", itemID: 5101, materialID: disjointerQuantityBigWinItemID, wantQuantity: 473},
		{name: "unknown ability keeps critical", itemID: 5102, materialID: disjointerQuantityBigWinItemID, wantQuantity: 473},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equipment, err := catalog.Equipment(tc.itemID)
			if err != nil {
				t.Fatal(err)
			}
			if equipment.WarriorsDungeon != tc.warriors {
				t.Fatalf("equipment=%+v: warriors_dungeon = %t, want %t", equipment, equipment.WarriorsDungeon, tc.warriors)
			}
			plan, err := catalog.PlanDisjointer(0, 1, equipment, false, func(int) int { return 0 })
			if err != nil {
				t.Fatal(err)
			}
			if got := disjointerQuantityMaterialCount(plan.Materials, tc.materialID); got != tc.wantQuantity {
				t.Fatalf("material=%d quantity = %d, want %d; plan=%+v", tc.materialID, got, tc.wantQuantity, plan)
			}
		})
	}
}

func TestEquipmentRejectsMissingInvalidAndOverflowingSellInputs(t *testing.T) {
	base := "[rarity] 6\n[minimum level] 90\n[grade] 90\n"
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "missing price", body: base},
		{name: "negative value", body: base + "[value] -1\n"},
		{name: "negative price", body: base + "[price] -1\n"},
		{name: "value times rate overflow", body: base + fmt.Sprintf("[value] %d\n", int64(math.MaxInt64))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := disjointerQuantityCatalog(t, map[int64]string{disjointerQuantityEquipmentID: tc.body})
			if _, err := catalog.Equipment(disjointerQuantityEquipmentID); !errors.Is(err, ErrExtractionInvalid) {
				t.Fatalf("Equipment() error = %v, want %v", err, ErrExtractionInvalid)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		table string
	}{
		{name: "missing rate table"},
		{name: "empty rate table", table: ""},
		{name: "zero rate", table: "[rate] 0\n"},
		{name: "negative rate", table: "[rate] -1\n"},
		{name: "rate exceeds int32", table: "[rate] 2147483648\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := disjointerQuantityTestSource{}
			if tc.table != "" || tc.name == "empty rate table" {
				source["equipment/pricetable.tbl"] = tc.table
			}
			catalog := &Catalog{source: source}
			if err := catalog.loadEquipmentSellRate(); err == nil {
				t.Fatal("invalid sell rate was accepted")
			}
		})
	}
}

func TestPlanDisjointerRejectsQuantityAboveMaxUint32(t *testing.T) {
	catalog := &Catalog{disjointer: &DisjointerConfig{
		BaseConst: 150,
		Results: map[disjointerResultKey]DisjointerResultRule{
			{MachineGrade: 0, Rarity: disjointerQuantityEquipmentRarity, State: 0}: {
				ItemID:     disjointerQuantityCubeItemID,
				Multiplier: 1,
			},
		},
	}}
	equipment := EquipmentMetadata{
		Rarity:   disjointerQuantityEquipmentRarity,
		Grade:    disjointerQuantityEquipmentGrade,
		SellGold: int64(math.MaxUint32+1) * 150,
	}
	if _, err := catalog.PlanDisjointer(0, 1, equipment, false, nil); !errors.Is(err, ErrExtractionInvalid) {
		t.Fatalf("PlanDisjointer() error = %v, want %v", err, ErrExtractionInvalid)
	}
}
