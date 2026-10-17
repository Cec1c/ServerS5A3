package worldmap

import "testing"

func TestStorePlacementMapLoadsImportedNPCsAndRejectsIncompleteData(t *testing.T) {
	for _, tc := range []struct {
		name, child string
		valid       bool
	}{
		{"imported-npc", "[NPC]\n10 `[left]` 500 300 0\n[/NPC]", true},
		{"incomplete-npc", "[NPC]\n10 `[left]` 500\n[/NPC]", false},
		{"missing-import", "", false},
		{"cycle", "[import script]\n`map/root.map`", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := npcEnterTownSource{"map/root.map": "[virtual movable area]\n0 0 1000 600\n[import script]\n`shared.map`"}
			if tc.child != "" {
				src["map/shared.map"] = tc.child
			}
			m, err := LoadTownPlacementMap(src, "ROOT.map")
			if (err == nil) != tc.valid {
				t.Fatalf("map=%+v err=%v", m, err)
			}
			if tc.valid && (len(m.NPCs) != 1 || m.NPCs[0].NPCID != 10 || m.NPCs[0].Position.X != 500 || len(m.VirtualMovableArea) != 4) {
				t.Fatalf("imported placement lost: %+v", m)
			}
		})
	}
	if _, err := LoadTownPlacementMap(nil, "map/root.map"); err == nil {
		t.Fatal("nil source accepted")
	}
}
