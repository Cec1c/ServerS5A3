package expertjob

import (
	"errors"
	"math"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestStorePlacementBoundariesAndInvalidData(t *testing.T) {
	m := worldmap.Map{VirtualMovableArea: []int64{0, 0, 1000, 1000}, NPCs: []worldmap.NPCSpawn{{NPCID: 1, Position: worldmap.Point3{X: 500, Y: 500}}}}
	for _, tc := range []struct {
		name string
		x, y int64
		want error
	}{
		{"npc", 500, 500, ErrStoreCommercialZone},
		{"inside-left", 421, 500, ErrStoreCommercialZone}, {"left-edge", 420, 500, nil},
		{"inside-right", 579, 500, ErrStoreCommercialZone}, {"right-edge", 580, 500, nil},
		{"inside-top", 500, 351, ErrStoreCommercialZone}, {"top-edge", 500, 350, nil},
		{"inside-bottom", 500, 649, ErrStoreCommercialZone}, {"bottom-edge", 500, 650, nil},
		{"map-origin", 0, 0, nil}, {"map-end", 1000, 1000, nil}, {"outside", 1001, 1000, ErrStorePointUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStorePlacement(m, tc.x, tc.y); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
	for _, rects := range [][]int64{nil, {0, 0, 10}, {0, 0, -1, 10}, {0, 0, 10, 10, math.MaxInt64, 0, 1, 1}} {
		if err := ValidateStorePlacement(worldmap.Map{VirtualMovableArea: rects}, 0, 0); !errors.Is(err, ErrStorePointUnavailable) {
			t.Fatalf("malformed rectangles %v accepted", rects)
		}
	}
	m.VirtualMovableArea = []int64{0, 0, 10, 10, 100, 100, 10, 10}
	m.NPCs = nil
	if err := ValidateStorePlacement(m, 105, 105); err != nil {
		t.Fatal("second actual rectangle rejected", err)
	}
	if err := ValidateStorePlacement(m, 50, 50); !errors.Is(err, ErrStorePointUnavailable) {
		t.Fatal("gap between rectangles accepted")
	}
}
