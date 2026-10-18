package darkknightcombo

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

func TestCleanSavedChildrenQuickSlotsPreservesRootsAndMovesChildren(t *testing.T) {
	layout := dnfrepo.SkillLayout{0: 100, 1: 46, 198: 47, 54: 9}
	got, moved, err := CleanSavedChildrenQuickSlots(
		layout,
		map[uint16]struct{}{100: {}},
		map[uint16]struct{}{46: {}, 47: {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := dnfrepo.SkillLayout{0: 100, 54: 9, 150: 46, 151: 47}
	if moved != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("moved=%d layout=%v want=%v", moved, got, want)
	}
	if !reflect.DeepEqual(layout, dnfrepo.SkillLayout{0: 100, 1: 46, 198: 47, 54: 9}) {
		t.Fatalf("input layout mutated: %v", layout)
	}
}

func TestCleanSavedChildrenQuickSlotsUsesStockSixPlusSixBoundary(t *testing.T) {
	layout := make(dnfrepo.SkillLayout)
	savedChildren := make(map[uint16]struct{})
	for slot := 0; slot < 12; slot++ {
		skillID := uint16(100 + slot)
		layout[slot] = skillID
		savedChildren[skillID] = struct{}{}
	}
	for slot := 198; slot < 204; slot++ {
		skillID := uint16(300 + slot - 198)
		layout[slot] = skillID
		savedChildren[skillID] = struct{}{}
	}

	got, moved, err := CleanSavedChildrenQuickSlots(
		layout,
		map[uint16]struct{}{},
		savedChildren,
	)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 12 {
		t.Fatalf("moved=%d want=12", moved)
	}
	for slot := 0; slot < 6; slot++ {
		if _, occupied := got[slot]; occupied {
			t.Fatalf("primary quick slot %d was not cleaned: %v", slot, got)
		}
	}
	for slot := 198; slot < 204; slot++ {
		if _, occupied := got[slot]; occupied {
			t.Fatalf("extension quick slot %d was not cleaned: %v", slot, got)
		}
	}
	for slot := 6; slot < 12; slot++ {
		want := uint16(100 + slot)
		if got[slot] != want {
			t.Fatalf("ordinary tree slot %d=%d want=%d; layout=%v", slot, got[slot], want, got)
		}
	}
}

func TestNormalizeFixedComboRootSlotsInstallsRootsAndMovesOrdinarySkillsToQWERTY(t *testing.T) {
	catalog := fixedComboRootTestCatalog(t)
	states := map[int64]dnfrepo.SkillState{
		5: {Level: 1, Enabled: true}, 8: {Level: 1, Enabled: true}, 46: {Level: 1, Enabled: true},
		118: {Level: 1, Enabled: true}, 119: {Level: 1, Enabled: true},
	}
	layout := dnfrepo.SkillLayout{0: 5, 1: 8, 2: 46, 198: 99, 150: 118, 151: 119}
	got, moved, err := NormalizeFixedComboRootSlots(catalog, states, layout)
	if err != nil {
		t.Fatal(err)
	}
	want := dnfrepo.SkillLayout{0: 118, 1: 119, 198: 99, 199: 5, 200: 8, 201: 46}
	if moved != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("moved=%d layout=%v want=%v", moved, got, want)
	}
	if !reflect.DeepEqual(layout, dnfrepo.SkillLayout{0: 5, 1: 8, 2: 46, 198: 99, 150: 118, 151: 119}) {
		t.Fatalf("input layout mutated: %v", layout)
	}
}

func TestNormalizeFixedComboRootSlotsOverflowsOrdinarySkillsIntoSkillTree(t *testing.T) {
	catalog := fixedComboRootTestCatalog(t)
	states := map[int64]dnfrepo.SkillState{
		5: {Level: 1, Enabled: true}, 8: {Level: 1, Enabled: true},
		118: {Level: 1, Enabled: true}, 119: {Level: 1, Enabled: true},
	}
	layout := dnfrepo.SkillLayout{0: 5, 1: 8}
	for slot := extensionQuickSlotFrom; slot < extensionQuickSlotEnd; slot++ {
		layout[slot] = uint16(100 + slot - extensionQuickSlotFrom)
	}
	layout[150] = 118
	layout[151] = 119

	got, moved, err := NormalizeFixedComboRootSlots(catalog, states, layout)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 || got[150] != 5 || got[151] != 8 || got[0] != 118 || got[1] != 119 {
		t.Fatalf("moved=%d layout=%v", moved, got)
	}
	for slot := 2; slot < ordinaryQuickSlotEnd; slot++ {
		if _, occupied := got[slot]; occupied {
			t.Fatalf("unlearned combo root slot %d remained occupied: %v", slot, got)
		}
	}
}

func fixedComboRootTestCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	source := comboTestSource{
		"skill/skilllist.lst":                 "9 `demonicswordman/skilllist.lst`\n",
		"skill/demonicswordman/skilllist.lst": "5 `five.skl`\n8 `eight.skl`\n46 `forty_six.skl`\n118 `comboset0.skl`\n119 `comboset1.skl`\n",
		"skill/demonicswordman/five.skl":      "[type] `[active]`\n",
		"skill/demonicswordman/eight.skl":     "[type] `[active]`\n",
		"skill/demonicswordman/forty_six.skl": "[type] `[active]`\n",
		"skill/demonicswordman/comboset0.skl": "[combo set]\n5 8\n[/combo set]\n",
		"skill/demonicswordman/comboset1.skl": "[combo set]\n46\n[/combo set]\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
