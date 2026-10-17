package worldmap

import (
	"context"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func TestTowerOfGriefMetadataMarkerAfterMaze(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int64
		present    bool
	}{
		{"empty", "[tower of grief]\n", 1, true},
		{"explicit zero", "[tower of grief]\n0\n", 0, true},
		{"explicit one", "[tower of grief]\n1\n", 1, true},
		{"explicit two", "[tower of grief]\n2\n", 2, true},
		{"absent", "", 0, false},
		{"text is not empty", "[tower of grief]\n`auto`\n", 0, false},
		{"symbols are not empty", "[tower of grief]\n{ }\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "[name]\n`Grief`\n[maze info]\n[size]\n1 1\n[greed]\n`BB`\n[start map]\n0 0\n[/start map]\n[boss map]\n0 0\n[/boss map]\n" + tc.body + "[fatigue result]\n4\n"
			doc := parseTestDocument(t, "dungeon/Towers/Grief/5115_001.dgn", text)
			d := ParseDungeon(5115, "dungeon/Towers/Grief/5115_001.dgn", doc)
			got, ok := d.Metadata.IntegerValues["tower of grief"]
			if ok != tc.present || ok && got != tc.want {
				t.Fatalf("marker=%d present=%t want=%d/%t; metadata=%+v", got, ok, tc.want, tc.present, d.Metadata)
			}
			if len(d.Mazes) != 1 || d.Metadata.IntegerValues["fatigue result"] != 4 {
				t.Fatalf("neighbor/maze changed: %+v", d)
			}
			for _, m := range d.Mazes {
				for _, sec := range m.UnknownSections {
					if sectionKey(sec.Name) == "tower of grief" {
						t.Fatal("dungeon marker was assigned to maze")
					}
				}
			}
		})
	}
}

func TestRealPVFTowerOfGriefMarkers(t *testing.T) {
	path := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{SkipMaps: true, SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, d := range table.Dungeons() {
		declared := false
		for _, sec := range d.SourceSections {
			if sectionKey(sec.Name) == "tower of grief" && len(sec.Tokens) == 0 {
				declared = true
			}
		}
		if !declared {
			continue
		}
		count++
		if d.Metadata.IntegerValues["tower of grief"] != 1 {
			t.Fatalf("lost marker: %d %s", d.ID, d.Path)
		}
		for _, m := range d.Mazes {
			for _, sec := range m.UnknownSections {
				if sectionKey(sec.Name) == "tower of grief" {
					t.Fatalf("marker assigned to maze: %d", d.ID)
				}
			}
		}
		if _, ok := d.Metadata.IntegerValues["tower of despair"]; ok {
			t.Fatalf("grief acquired despair identity: %d", d.ID)
		}
	}
	if count != 100 {
		t.Fatalf("indexed grief marker count=%d want100", count)
	}
	first, ok := table.FindDungeon(5115)
	if !ok || !strings.EqualFold(first.Path, "dungeon/towers/grief/5115_001.dgn") {
		t.Fatalf("first grief identity=%+v", first)
	}
	t.Logf("Verified %d independent grief markers", count)
}
