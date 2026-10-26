package worldmap

import (
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
)

func TestFriendlyGuildPracticeNativeTagsAndMetadataScope(t *testing.T) {
	for _, beforeMaze := range []bool{true, false} {
		for _, agit := range []int{0, 1, -1, 2} {
			for _, friendly := range []bool{false, true} {
				t.Run(fmt.Sprintf("beforeMaze=%t/agit=%d/friendly=%t", beforeMaze, agit, friendly), func(t *testing.T) {
					text := fmt.Sprintf("[agit war dungeon]\n%d\n", agit)
					if friendly {
						text += "[friendly agit dungeon]\n"
					}
					maze := "[maze info]\n[greed]\n1 1\n"
					if beforeMaze {
						text += maze
					} else {
						text = maze + text
					}
					doc, err := pvf.Parse("practice.dgn", text)
					if err != nil {
						t.Fatal(err)
					}
					dungeon := ParseDungeon(8510, "practice.dgn", doc)
					if IsFriendlyGuildPractice(dungeon) != (agit != 0 && friendly) {
						t.Fatalf("flags lost or inferred from ID: %+v", dungeon.Metadata)
					}
				})
			}
		}
	}
	if IsFriendlyGuildPractice(Dungeon{ID: 8510}) {
		t.Fatal("ID alone is not a tag contract")
	}
}
