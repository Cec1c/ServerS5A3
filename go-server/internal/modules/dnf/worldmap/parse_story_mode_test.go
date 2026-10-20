package worldmap

import "testing"

func TestParseDungeonStoryModeBlockCloneAndMazeOwnership(t *testing.T) {
	text := "[name]\n`Story Dungeon`\n" +
		"[maze info]\n[size]\n1 1\n[greed]\n`A`\n[map specification]\n`map` 0 0 100\n[start map]\n0 0\n[boss map]\n0 0\n" +
		"[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n" +
		"[WARLIKE]\n[ALL]\n100 0\n[/WARLIKE]\n" +
		"[quest list]\n3149 3150 {7=``}3151\n[independent rate]\n100 50 3152\n[/independent rate]\n[/quest list]\n" +
		"[/story mode]\n"
	doc := parseTestDocument(t, "dungeon/story_mode.dgn", text)
	dungeon := ParseDungeon(90, "dungeon/story_mode.dgn", doc)
	mode := dungeon.Metadata.StoryMode
	if mode == nil {
		t.Fatal("story mode block was not parsed")
	}
	if mode.Malformed {
		t.Fatalf("valid story mode marked malformed: %+v", mode)
	}
	if mode.DifficultySize != 1 {
		t.Fatalf("difficulty size = %d, want 1", mode.DifficultySize)
	}
	if got := mode.IncreaseExperienceRates; len(got) != 2 || got[0] != 100 || got[1] != 30 {
		t.Fatalf("increase exp rates = %v, want [100 30]", got)
	}
	if got := mode.QuestIDs; len(got) != 3 || got[0] != 3149 || got[1] != 3150 || got[2] != 3151 {
		t.Fatalf("quest ids = %v, want [3149 3150 3151]", got)
	}
	for _, questID := range mode.QuestIDs {
		if questID == 3152 {
			t.Fatal("independent-rate quest 3152 entered the approved QuestIDs")
		}
	}
	if len(dungeon.Mazes) != 1 {
		t.Fatalf("mazes = %d, want 1", len(dungeon.Mazes))
	}
	for _, section := range dungeon.Mazes[0].UnknownSections {
		switch sectionKey(section.Name) {
		case "story mode", "difficulty size", "increase exp rate", "quest list", "independent rate", "warlike", "all":
			t.Fatalf("story-mode section leaked into maze unknown sections: %+v", dungeon.Mazes[0].UnknownSections)
		}
	}
	for _, extension := range dungeon.Mazes[0].Extensions {
		switch sectionKey(extension.Name) {
		case "story mode", "difficulty size", "increase exp rate", "quest list", "independent rate", "warlike", "all":
			t.Fatalf("story-mode section leaked into maze extensions: %+v", dungeon.Mazes[0].Extensions)
		}
	}
	unknownPreserved := false
	for _, section := range dungeon.UnknownSections {
		if sectionKey(section.Name) == "warlike" {
			unknownPreserved = true
		}
	}
	if !unknownPreserved {
		t.Fatalf("unknown story-mode child was not preserved: %+v", dungeon.UnknownSections)
	}
	independentRaw := false
	independentExtension := false
	for _, section := range dungeon.UnknownSections {
		if sectionKey(section.Name) == "independent rate" {
			independentRaw = true
		}
	}
	for _, extension := range dungeon.Extensions {
		if sectionKey(extension.Name) != "independent rate" {
			continue
		}
		independentExtension = true
		if extension.Kind != SectionIntegers || len(extension.Integers) != 3 || extension.Integers[0] != 100 || extension.Integers[1] != 50 || extension.Integers[2] != 3152 {
			t.Fatalf("independent-rate raw values lost: %+v", extension)
		}
	}
	if !independentRaw || !independentExtension {
		t.Fatalf("independent-rate block was not retained as unknown raw data: raw=%t extension=%t", independentRaw, independentExtension)
	}

	cloned := cloneDungeon(dungeon)
	if cloned.Metadata.StoryMode == nil || cloned.Metadata.StoryMode == dungeon.Metadata.StoryMode {
		t.Fatal("clone did not allocate a separate StoryMode pointer")
	}
	cloned.Metadata.StoryMode.IncreaseExperienceRates[0] = 999
	cloned.Metadata.StoryMode.QuestIDs[0] = 999
	if dungeon.Metadata.StoryMode.IncreaseExperienceRates[0] != 100 || dungeon.Metadata.StoryMode.QuestIDs[0] != 3149 {
		t.Fatal("clone slices alias the original story mode")
	}
	dungeon.Metadata.StoryMode.IncreaseExperienceRates[1] = 888
	if cloned.Metadata.StoryMode.IncreaseExperienceRates[1] != 30 {
		t.Fatal("original mutation changed the clone")
	}
}

func TestParseDungeonStoryModeStrictMalformed(t *testing.T) {
	valid := "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n" +
		"[quest list]\n3149\n[/quest list]\n[/story mode]\n"
	tests := []struct {
		name string
		text string
	}{
		{"duplicate difficulty size", "[story mode]\n[difficulty size]\n1\n[difficulty size]\n1\n[increase exp rate]\n100 30\n[quest list]\n3149\n[/quest list]\n[/story mode]\n"},
		{"non-integer difficulty size", "[story mode]\n[difficulty size]\none\n[increase exp rate]\n100 30\n[quest list]\n3149\n[/quest list]\n[/story mode]\n"},
		{"negative multiplier", "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 -1\n[quest list]\n3149\n[/quest list]\n[/story mode]\n"},
		{"missing increase exp rate", "[story mode]\n[difficulty size]\n1\n[quest list]\n3149\n[/quest list]\n[/story mode]\n"},
		{"dimension mismatch", "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30 20\n[quest list]\n3149\n[/quest list]\n[/story mode]\n"},
		{"duplicate quest", "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n[quest list]\n3149 3149\n[/quest list]\n[/story mode]\n"},
		{"non-integer quest", "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n[quest list]\n3149 nope\n[/quest list]\n[/story mode]\n"},
		{"missing quest list", "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n[/story mode]\n"},
		{"duplicate story mode block", valid + valid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := parseTestDocument(t, "dungeon/bad_story_mode.dgn", test.text)
			dungeon := ParseDungeon(91, "dungeon/bad_story_mode.dgn", doc)
			if dungeon.Metadata.StoryMode == nil {
				t.Fatalf("story mode block was dropped: %+v", dungeon.Metadata)
			}
			if !dungeon.Metadata.StoryMode.Malformed {
				t.Fatalf("malformed story mode accepted: %+v", dungeon.Metadata.StoryMode)
			}
		})
	}
}

func TestParseDungeonStoryModeIndependentRateIsOpaque(t *testing.T) {
	text := "[story mode]\n[difficulty size]\n1\n[increase exp rate]\n100 30\n" +
		"[quest list]\n3149\n[independent rate]\n100 nope 3152\n[/independent rate]\n[/quest list]\n[/story mode]\n"
	doc := parseTestDocument(t, "dungeon/story_mode_independent.dgn", text)
	dungeon := ParseDungeon(94, "dungeon/story_mode_independent.dgn", doc)
	mode := dungeon.Metadata.StoryMode
	if mode == nil || mode.Malformed {
		t.Fatalf("opaque independent-rate data invalidated the regular story fields: %+v", mode)
	}
	if len(mode.QuestIDs) != 1 || mode.QuestIDs[0] != 3149 {
		t.Fatalf("approved quest ids = %v, want [3149]", mode.QuestIDs)
	}
	found := false
	for _, extension := range dungeon.Extensions {
		if sectionKey(extension.Name) == "independent rate" {
			found = true
			if extension.Kind != SectionMixed || len(extension.Tokens) == 0 {
				t.Fatalf("independent-rate raw tokens lost: %+v", extension)
			}
		}
	}
	if !found {
		t.Fatalf("independent-rate block was not preserved as unknown data: %+v", dungeon.Extensions)
	}
}

func TestParseDungeonWithoutStoryModeLeavesNil(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/plain_story.dgn", "[name]\n`Plain`\n[fatigue]\n8\n")
	dungeon := ParseDungeon(92, "dungeon/plain_story.dgn", doc)
	if dungeon.Metadata.StoryMode != nil {
		t.Fatalf("unexpected story mode: %+v", dungeon.Metadata.StoryMode)
	}
}

func TestParseDungeonStoryModeRejectsZeroSize(t *testing.T) {
	text := "[story mode]\n[difficulty size]\n0\n[increase exp rate]\n100\n" +
		"[quest list]\n3149\n[/quest list]\n[/story mode]\n"
	doc := parseTestDocument(t, "dungeon/story_mode_zero.dgn", text)
	mode := ParseDungeon(93, "dungeon/story_mode_zero.dgn", doc).Metadata.StoryMode
	if mode == nil || !mode.Malformed {
		t.Fatalf("zero-size story mode was not rejected: %+v", mode)
	}
	if mode.DifficultySize != 0 || len(mode.IncreaseExperienceRates) != 1 || mode.IncreaseExperienceRates[0] != 100 {
		t.Fatalf("zero-size parsed values were not preserved: %+v", mode)
	}
}
