package worldmap

import (
	"os"
	"strings"
	"testing"
)

// The current resource has its own identity gate. The original paired-resource
// test remains unchanged in scope and keeps rejecting a mismatched archive.
func TestRealPVFCurrentTowerStartResourceFloors(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNF_WORLDMAP_CURRENT_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNF_WORLDMAP_CURRENT_PVF_SMOKE to the current Script.pvf")
	}
	const expected = "94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7"
	if actual := rest25RealPVFHash(t, path); actual != expected {
		t.Fatalf("current PVF mismatch: sha256=%s want=%s", actual, expected)
	}
	// Reuse every 100-floor, room, ownership, spawn and pool assertion; the
	// separate current-resource case does not relax the older expectations.
	checkRealPVFTowerStartResourceFloors(t, path)
}
