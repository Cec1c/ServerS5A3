package worldmap

import "testing"

func TestParseDungeonStartFatigueCost(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/guild.dgn", `
[maze info]
[size]
1 1
[map specification]
`+"`map`"+` 0 0 8501
[start map]
0 0
[boss map]
0 0
[use fatigue only start dungeon]
8
[/use fatigue only start dungeon]
`)
	dungeon := ParseDungeon(8500, "dungeon/guild.dgn", doc)
	if got := dungeon.Metadata.IntegerValues[DungeonUseFatigueOnlyStartKey]; got != 8 {
		t.Fatalf("start fatigue cost=%d metadata=%+v diagnostics=%+v", got, dungeon.Metadata, dungeon.Diagnostics)
	}
	if dungeon.Metadata.Flags[DungeonUseFatigueOnlyStartKey] {
		t.Fatal("integer start fatigue declaration was also recorded as a flag")
	}

	legacy := parseTestDocument(t, "dungeon/legacy.dgn", "[use fatigue only start dungeon]\n")
	parsed := ParseDungeon(1, "dungeon/legacy.dgn", legacy)
	if !parsed.Metadata.Flags[DungeonUseFatigueOnlyStartKey] {
		t.Fatal("legacy bare presence marker was not preserved")
	}
}
