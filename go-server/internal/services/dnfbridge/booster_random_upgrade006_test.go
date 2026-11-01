package dnfbridge

import (
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"testing"
)

func TestRandomEquipment006MalformedTables(t *testing.T) {
	for _, table := range []string{"", "1", "1 99", "1 101", "1 50 1 50", "-1 100", "32 100", "255 100", "1 0 2 100", "1 -1 2 101", "`1` 100", "1 100.0"} {
		doc, e := pvf.Parse("stackable/test.stk", "[booster select category] 0\n[booster equipment upgrade] 255\n[random probability] "+table+"\n[/random probability]\n[equipment] 700 1\n[/booster select category]")
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = parseCurrentBoosterSelectionCatalog(doc); e == nil {
			t.Fatalf("accepted malformed random table %q", table)
		}
	}
	for _, text := range []string{
		"[booster equipment upgrade] 12\n[random probability] 1 100\n",
		"[booster equipment upgrade] 255\n[random probability] 1 100\n[/random probability]\n[random probability]\n",
		"[booster equipment upgrade] 255\n[random probability] 1 100\n[/random probability]\n[random probability] 2 100\n",
	} {
		doc, e := pvf.Parse("stackable/test.stk", "[booster select category] 0\n"+text+"[equipment] 700 1\n[/booster select category]")
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = parseCurrentBoosterSelectionCatalog(doc); e == nil {
			t.Fatal("misplaced/duplicate probability accepted")
		}
	}
}

func TestRandomEquipment006CategoryScopeAndCounts(t *testing.T) {
	text := "[booster select category] 0\n[booster equipment upgrade] 255\n[random probability] 1 30 12 70\n[equipment] 700 1\n[/booster select category]\n[booster select category] 1\n[booster equipment upgrade] 255\n[random probability] 2 100\n[equipment] 701 1\n[/booster select category]\n[booster select category] 2\n[equipment] 702 1\n[/booster select category]"
	doc, e := pvf.Parse("stackable/test.stk", text)
	if e != nil {
		t.Fatal(e)
	}
	_, cats, e := parseCurrentBoosterSelectionCatalog(doc)
	if e != nil {
		t.Fatal(e)
	}
	if cats[0][0].EquipmentUpgradeTable[0] != (currentBoosterUpgradeChance{1, 30}) || cats[1][0].EquipmentUpgradeTable[0] != (currentBoosterUpgradeChance{2, 100}) || cats[2][0].EquipmentUpgradeTable != (currentBoosterUpgradeTable{}) || cats[2][0].EquipmentUpgrade != 0 {
		t.Fatal("category table leaked")
	}
	for roll := uint32(0); roll < 100; roll++ {
		level, e := currentBoosterUpgradeAt(cats[0][0].EquipmentUpgradeTable, roll)
		want := byte(1)
		if roll >= 30 {
			want = 12
		}
		if e != nil || level != want {
			t.Fatal("wrong boundary")
		}
	}
	if _, e := currentBoosterUpgradeAt(cats[0][0].EquipmentUpgradeTable, 100); e == nil {
		t.Fatal("out of range roll")
	}
	if _, e := currentBoosterUpgradeAt(currentBoosterUpgradeTable{}, 0); e == nil {
		t.Fatal("empty table")
	}
	for _, tail := range []string{"[equipment] 700 2", "[equipment] 700 1\n[/booster select category]\n[booster select category] 1\n[booster equipment upgrade] 255\n[equipment] 701 1"} {
		doc, e := pvf.Parse("stackable/test.stk", "[booster select category] 0\n[booster equipment upgrade] 255\n[random probability] 1 100\n"+tail+"\n[/booster select category]")
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = parseCurrentBoosterSelectionCatalog(doc); e == nil {
			t.Fatal("unsupported count or missing per-category table accepted")
		}
	}
}
