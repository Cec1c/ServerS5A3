package worldmap

import "testing"

func TestInfernoEntryCostSectionsRemainDungeonMetadata(t *testing.T) {
	doc := parseTestDocument(t, "inferno.dgn", "[hell dungeon]\n1\n[hell party required item count]\n30\n[inferno dungeon]\n")
	d := ParseDungeon(7539, "inferno.dgn", doc)
	if !d.Metadata.Flags["inferno dungeon"] || d.Metadata.IntegerValues["hell party required item count"] != 30 || d.Metadata.IntegerValues["hell dungeon"] != 1 {
		t.Fatalf("inferno metadata lost: %+v", d.Metadata)
	}
}
