package worldmap

import (
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealScriptPVFStoryModeParsesBlocksAndKeepsMazeOwnership(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to run the real Script.pvf story-mode regression")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	found := 0
	for _, file := range archive.Files() {
		if !strings.HasSuffix(strings.ToLower(file.ArchivePath), ".dgn") {
			continue
		}
		text, err := archive.ReadText(file.ArchivePath)
		if err != nil || !strings.Contains(strings.ToLower(text), "[story mode]") {
			continue
		}
		doc, err := ParseDocument(file.ArchivePath, text)
		if err != nil {
			t.Fatalf("parse %s: %v", file.ArchivePath, err)
		}
		dungeon := ParseDungeon(0, file.ArchivePath, doc)
		mode := dungeon.Metadata.StoryMode
		if mode == nil {
			t.Fatalf("real story mode block was not parsed: %s", file.ArchivePath)
		}
		if mode.Malformed {
			t.Fatalf("real story mode marked malformed: %s %+v", file.ArchivePath, mode)
		}
		if mode.DifficultySize <= 0 || len(mode.IncreaseExperienceRates) != int(mode.DifficultySize)+1 {
			t.Fatalf("real story mode dimension mismatch: %s size=%d rates=%v", file.ArchivePath, mode.DifficultySize, mode.IncreaseExperienceRates)
		}
		if len(mode.QuestIDs) == 0 {
			t.Fatalf("real story mode has no quest ids: %s %+v", file.ArchivePath, mode)
		}
		if strings.EqualFold(file.ArchivePath, "dungeon/act1/grakkarak.dgn") {
			if mode.DifficultySize != 1 || len(mode.IncreaseExperienceRates) != 2 || mode.IncreaseExperienceRates[0] != 100 || mode.IncreaseExperienceRates[1] != 30 {
				t.Fatalf("grakkarak story mode = %+v", mode)
			}
			if len(mode.QuestIDs) != 1 || mode.QuestIDs[0] != 3149 {
				t.Fatalf("grakkarak quest ids = %v", mode.QuestIDs)
			}
		}
		for _, maze := range dungeon.Mazes {
			for _, section := range maze.UnknownSections {
				if storyModeSectionName(sectionKey(section.Name)) {
					t.Fatalf("story-mode section leaked into maze %s: %s", file.ArchivePath, section.Name)
				}
			}
			for _, extension := range maze.Extensions {
				if storyModeSectionName(sectionKey(extension.Name)) {
					t.Fatalf("story-mode extension leaked into maze %s: %s", file.ArchivePath, extension.Name)
				}
			}
		}
		found++
	}
	if found == 0 {
		t.Fatal("real Script.pvf contains no [story mode] blocks")
	}
	t.Logf("parsed %d real story-mode dungeons", found)
}

func storyModeSectionName(key string) bool {
	switch key {
	case "story mode", "difficulty size", "increase exp rate", "quest list", "independent rate":
		return true
	default:
		return false
	}
}
