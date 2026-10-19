package darkknightcombo

import (
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealScriptPVFDarkKnightFixedComboRootsOccupyASDFGH(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify Dark Knight combo roots")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}

	states := make(map[int64]dnfrepo.SkillState)
	layout := make(dnfrepo.SkillLayout)
	rootCount := 0
	ordinaryCount := 0
	for _, definition := range catalog.SkillsForJob(Job) {
		if slot, root := fixedComboRootSlot(definition.Path); root {
			states[int64(definition.ID)] = dnfrepo.SkillState{Level: 1, Enabled: true}
			layout[150+slot] = definition.ID
			rootCount++
			continue
		}
		if definition.Active && ordinaryCount < 3 {
			states[int64(definition.ID)] = dnfrepo.SkillState{Level: 1, Enabled: true}
			layout[ordinaryCount] = definition.ID
			ordinaryCount++
		}
	}
	if rootCount != ordinaryQuickSlotEnd || ordinaryCount != 3 {
		t.Fatalf("real PVF roots=%d ordinary fixtures=%d", rootCount, ordinaryCount)
	}

	normalized, moved, err := NormalizeFixedComboRootSlots(catalog, states, layout)
	if err != nil {
		t.Fatal(err)
	}
	if moved != ordinaryCount {
		t.Fatalf("moved=%d want=%d layout=%v", moved, ordinaryCount, normalized)
	}
	for slot := 0; slot < ordinaryQuickSlotEnd; slot++ {
		skillID, found := normalized[slot]
		if !found {
			t.Fatalf("real PVF combo slot %d is empty: %v", slot, normalized)
		}
		definition, found := catalog.Find(Job, skillID)
		rootSlot, root := fixedComboRootSlot(definition.Path)
		if !found || !root || rootSlot != slot {
			t.Fatalf("slot %d skill=%d path=%q rootSlot=%d root=%t", slot, skillID, definition.Path, rootSlot, root)
		}
	}
	for slot := extensionQuickSlotFrom; slot < extensionQuickSlotFrom+ordinaryCount; slot++ {
		if _, found := normalized[slot]; !found {
			t.Fatalf("ordinary Dark Knight skill was not preserved at QWERTY slot %d: %v", slot, normalized)
		}
	}
}
