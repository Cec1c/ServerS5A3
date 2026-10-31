package worldmap

import "testing"

func TestParseDungeonHiddenBossMazeBelongsToItsMaze(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/hidden_boss_mazes.dgn", `
[name]
`+pvfBacktick+`Hidden Boss Mazes`+pvfBacktick+`
[maze info]
[size]
1 1
[map specification]
`+pvfBacktick+`map`+pvfBacktick+` 0 0 100
[start map]
0 0
[boss map]
0 0
[quest connection]
0 900 -1
[maze info]
[size]
1 1
[hidden boss maze]
[map specification]
`+pvfBacktick+`boss`+pvfBacktick+` 0 0 101
[start map]
0 0
[boss map]
0 0
[quest connection]
0 901 -1
[maze info]
[size]
1 1
[map specification]
`+pvfBacktick+`map`+pvfBacktick+` 0 0 102
[start map]
0 0
[boss map]
0 0
[quest connection]
0 902 -1
`)

	dungeon := ParseDungeon(7165, "dungeon/dimensioncrack/blackearth_michaela/blackearth_michaela_g1.dgn", doc)
	if len(dungeon.Mazes) != 3 {
		t.Fatalf("mazes = %d, want 3: %+v", len(dungeon.Mazes), dungeon.Mazes)
	}
	wantHidden := []bool{false, true, false}
	wantQuest := []int64{900, 901, 902}
	for index, maze := range dungeon.Mazes {
		if maze.HiddenBoss != wantHidden[index] {
			t.Errorf("maze[%d].HiddenBoss = %v, want %v", index, maze.HiddenBoss, wantHidden[index])
		}
		if len(maze.QuestConnection) != 3 || maze.QuestConnection[0] != 0 || maze.QuestConnection[1] != wantQuest[index] || maze.QuestConnection[2] != -1 {
			t.Errorf("maze[%d].QuestConnection = %v, want [0 %d -1]", index, maze.QuestConnection, wantQuest[index])
		}
		for _, section := range maze.UnknownSections {
			if sectionKey(section.Name) == "hidden boss maze" {
				t.Errorf("maze[%d] kept typed hidden marker in UnknownSections", index)
			}
		}
		for _, extension := range maze.Extensions {
			if sectionKey(extension.Name) == "hidden boss maze" {
				t.Errorf("maze[%d] kept typed hidden marker in Extensions", index)
			}
		}
	}
	if _, ok := dungeon.Metadata.Flags["hidden boss maze"]; ok {
		t.Fatalf("hidden boss maze leaked into DungeonMetadata.Flags: %+v", dungeon.Metadata.Flags)
	}
}
