package dnfbridge

import (
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sort"
	"testing"
)

func TestRealPVFUpgradeRareEquipmentKeepsAmplifyPenalty(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(28309)
	if err != nil {
		t.Fatal(err)
	}
	if def.Rarity < 2 {
		t.Fatalf("equipment fixture rarity=%d", def.Rarity)
	}
	service := &Service{initialEquipmentArchive: archive, pvfItemCatalog: catalog}
	resolver := service.alignedUpgradePolicyResolver()
	for _, tc := range []struct {
		mode               string
		level, kind, value int
	}{
		{"amplify", 8, 2, 8}, {"amplify", 9, 2, 9}, {"amplify", 10, 3, 0},
		{"reinforce", 8, 1, 0}, {"reinforce", 10, 2, 3}, {"reinforce", 12, 3, 0},
	} {
		got, err := resolver(tc.mode, 28309, tc.level)
		if err != nil || got.PenaltyType != tc.kind || got.PenaltyValue != tc.value {
			t.Errorf("%s +%d rarity=%d policy=%+v err=%v want=(%d,%d)", tc.mode, tc.level, def.Rarity, got, err, tc.kind, tc.value)
		}
	}
}

func TestRealPVFUpgradePenaltyRespectsEveryEquipmentRarity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 0)
	for id, ref := range catalog.itemRefs {
		if ref.kind == dungeonDropItemEquipment {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	fixtures := map[int64]uint32{}
	for _, id := range ids {
		def, err := catalog.ResolveItem(uint32(id))
		if err != nil {
			continue
		}
		if def.Rarity >= 0 && def.Rarity <= 6 {
			if _, found := fixtures[def.Rarity]; !found {
				fixtures[def.Rarity] = uint32(id)
			}
		}
		if len(fixtures) == 7 {
			break
		}
	}
	if len(fixtures) != 7 {
		t.Fatalf("missing real equipment rarity: %v", fixtures)
	}
	resolver := (&Service{initialEquipmentArchive: archive, pvfItemCatalog: catalog}).alignedUpgradePolicyResolver()
	for rarity := int64(0); rarity <= 6; rarity++ {
		id := fixtures[rarity]
		for _, level := range []int{7, 8, 9, 10} {
			kind, value := 2, level
			if level == 10 || rarity <= 1 && level >= 8 {
				kind, value = 3, 0
			}
			got, err := resolver("amplify", int64(id), level)
			if err != nil || got.PenaltyType != kind || kind == 2 && got.PenaltyValue != value {
				t.Fatalf("rarity=%d item=%d level=%d policy=%+v err=%v want=%d/%d", rarity, id, level, got, err, kind, value)
			}
			if kind != 3 && (got.DestroyBonusItemID != 0 || got.DestroyBonusCount != 0) {
				t.Fatalf("non-destroy penalty granted compensation: %+v", got)
			}
		}
		t.Logf("real equipment rarity=%d item=%d", rarity, id)
	}
	for _, id := range []int64{0, -1, 1 << 32, 3037} {
		if _, err := resolver("amplify", id, 8); err == nil {
			t.Fatalf("invalid policy target accepted: %d", id)
		}
	}
}
