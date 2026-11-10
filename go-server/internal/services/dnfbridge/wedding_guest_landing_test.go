package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	archive "longheng.io/server/internal/platform/pvf"
)

func TestWeddingGuestLandingRejectsMissingMalformedOrOverflowingPVF(t *testing.T) {
	for _, rectangle := range [][]int64{nil, {1, 2, 3}, {1, 2, 3, 4, 5}, {1, 2, 0, 4}, {1, 2, 3, -1}, {32767, 2, 4, 6}, {-1, 2, 4, 6}} {
		if _, err := currentWeddingGuestLanding(worldmap.Map{VirtualMovableArea: rectangle}); err == nil {
			t.Fatalf("invented a landing for invalid map rectangle %v", rectangle)
		}
	}
}

func TestWeddingGuestLandingCurrentPVFMatchesFixed86Policy(t *testing.T) {
	path := os.Getenv("DNF_WEDDING_TEST_PVF")
	if path == "" {
		t.Skip("requires paired runtime PVF")
	}
	source, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), source, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	area, found := towns.FindArea(39, 8)
	if !found {
		t.Fatal("Church area missing")
	}
	church, err := worldmap.LoadTownPlacementMap(source, area.MapPath)
	if err != nil {
		t.Fatal(err)
	}
	position, err := currentWeddingGuestLanding(church)
	if err != nil || position.X != 323 || position.Y != 332 {
		t.Fatalf("fixed 86/PVF landing mismatch: %+v/%v; rectangle=%v", position, err, church.VirtualMovableArea)
	}
}
