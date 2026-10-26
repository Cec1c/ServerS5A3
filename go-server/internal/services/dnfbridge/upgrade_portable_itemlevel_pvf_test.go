package dnfbridge

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFPortableConsumableUsableItemLevel(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolveCurrentUpgradeTicketMetadata(catalog, archive, 10164868, 28309)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.PortableMode != "amplify" || !resolution.MaterialUsableItemLevelSet || resolution.MaterialUsableItemLevelMin != 1 || resolution.MaterialUsableItemLevelMax != 90 || resolution.TargetMinimumLevel != 65 {
		t.Fatalf("real portable metadata=%+v", resolution)
	}
}

func TestPortableItemLevelMetadataRound15(t *testing.T) {
	for _, tc := range []struct {
		name, window, minimum string
		set                   bool
		min, max, target      int
	}{
		{"present", "[check usable itemlevel] 55 90\n", "[minimum level] 90\n", true, 55, 90, 90},
		{"missing", "", "", false, -1, -1, -1},
		{"empty", "[check usable itemlevel]\n", "[minimum level] 90\n", true, -2, -2, 90},
		{"nonnumeric", "[check usable itemlevel] `wrong`\n", "[minimum level] 90\n", true, -2, -2, 90},
		{"zero", "[check usable itemlevel] 0 0\n", "[minimum level] 0\n", true, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, source := newUpgradeTicketTestCatalog(t)
			source["stackable/ticket/plus10.stk"] = "[stackable type] `[town and dungeon]` 0\n[action type] `[portable amplify]` 0 30\n[/action type]\n" + tc.window
			source["equipment/weapon/test_sword.equ"] = "[equipment type] `[weapon]`\n[grade] 96\n" + tc.minimum
			r, err := resolveCurrentUpgradeTicketMetadata(catalog, source, 9100, 700)
			if err != nil {
				t.Fatal(err)
			}
			if r.PortableMode != "amplify" || r.MaterialUsableItemLevelSet != tc.set || r.MaterialUsableItemLevelMin != tc.min || r.MaterialUsableItemLevelMax != tc.max || r.TargetMinimumLevel != tc.target {
				t.Fatalf("metadata=%+v", r)
			}
		})
	}
}
