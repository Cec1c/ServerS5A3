package worldmap

import "testing"

func TestAreaFreePassTablesRemainDistinctAndCloneIndependent(t *testing.T) {
	doc := parseTestDocument(t, "worldmap/test.wdm", "[hell freepass item]\n10 1\n[/hell freepass item]\n[inferno freepass item]\n20 1\n[/inferno freepass item]\n[particularhell freepass item]\n30 1 31 2\n[/particularhell freepass item]\n")
	area := ParseArea(39, "worldmap/test.wdm", doc)
	if len(area.UnknownSections) != 0 || len(area.Diagnostics) != 0 || len(area.HellFreePassItems) != 1 || len(area.InfernoFreePassItems) != 1 || len(area.ParticularHellFreePassItems) != 2 {
		t.Fatalf("area=%+v", area)
	}
	cloned := cloneArea(area)
	cloned.InfernoFreePassItems[0].ItemID = 99
	cloned.ParticularHellFreePassItems[0].Count = 99
	if area.InfernoFreePassItems[0].ItemID != 20 || area.ParticularHellFreePassItems[0].Count != 1 || area.HellFreePassItems[0].ItemID != 10 {
		t.Fatal("ticket tables aliased")
	}
}
