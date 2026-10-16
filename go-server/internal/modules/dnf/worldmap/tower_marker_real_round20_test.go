package worldmap

import (
	"context"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Every indexed dungeon with the actual empty marker must preserve that marker
// in metadata. This test does not assert floor rewards or native tower entry.
func TestRealPVFTowerOfDespairMarkersRound20(t *testing.T) {
	path := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires the real dungeon archive")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{SkipMaps: true, SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	empty := 0
	for _, dungeon := range table.Dungeons() {
		for _, section := range dungeon.SourceSections {
			if sectionKey(section.Name) != "tower of despair" || section.Block || len(section.Tokens) != 0 {
				continue
			}
			empty++
			if got, ok := dungeon.Metadata.IntegerValues["tower of despair"]; !ok || got != 1 {
				t.Fatalf("dungeon %d %s lost empty tower marker: got=%d present=%t", dungeon.ID, dungeon.Path, got, ok)
			}
			for _, diagnostic := range dungeon.Diagnostics {
				if sectionKey(diagnostic.Section) == "tower of despair" {
					t.Fatalf("valid empty marker diagnosed in %d: %+v", dungeon.ID, diagnostic)
				}
			}
		}
	}
	first, ok := table.FindDungeon(11008)
	if !ok || !strings.EqualFold(first.Path, "dungeon/towers/ztowerofdespair001.dgn") || first.Metadata.IntegerValues["tower of despair"] != 1 {
		t.Fatalf("actual first-floor marker not preserved: %+v", first)
	}
	if empty != 100 {
		t.Fatalf("expected the fixed archive's 100 empty tower markers, inspected %d", empty)
	}
	t.Logf("verified %d indexed empty tower markers", empty)
}
