package raid

import (
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strings"
	"testing"
)

const sanctumMazeTestResource = "[CARD SET MAP INFO]\n0 0 0 3 3 3 6 6 6 9 9 9 12 12 12\n[/CARD SET MAP INFO]\n[BIRTH SANCTUM]\n5 5054\n[EXTINCTION SANCTUM]\n5 5055\n[SPREAD SANCTUM]\n5 5056\n"

func TestLukeSanctumMazeResourceRejectsMalformedContract(t *testing.T) {
	for _, row := range []struct{ name, text string }{
		{"missing", ""},
		{"incomplete", strings.Replace(sanctumMazeTestResource, "0 0 0 3", "0 0 3", 1)},
		{"negative", strings.Replace(sanctumMazeTestResource, "0 0 0", "-1 0 0", 1)},
		{"overlap", strings.Replace(sanctumMazeTestResource, "3 3 3", "2 3 3", 1)},
		{"wrong_owner", strings.Replace(sanctumMazeTestResource, "5055", "5054", 1)},
		{"duplicate", sanctumMazeTestResource + "[CARD SET MAP INFO]\n0 0 0\n"},
		{"string", strings.Replace(sanctumMazeTestResource, "0 0 0", "`zero` 0 0", 1)},
	} {
		t.Run(row.name, func(t *testing.T) {
			if _, err := LoadLukeSanctumMazeConfiguration(admissionSource{"etc/raid/luke/luke.etc": row.text}); err == nil {
				t.Fatal("malformed contract accepted")
			}
		})
	}
}

func TestRealPVFLukeSanctumMazeResourceTripletMapping(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadLukeSanctumMazeConfiguration(a)
	if err != nil {
		t.Fatal(err)
	}
	expected := [][3]uint32{{0, 0, 0}, {3, 3, 3}, {6, 6, 6}, {9, 9, 9}, {12, 12, 12}}
	if !reflect.DeepEqual(c.Records, expected) {
		t.Fatal(c.Records)
	}
	for _, dungeon := range c.Dungeons {
		for maze := 0; maze < 15; maze++ {
			record, err := c.RecordForMaze(dungeon, maze, 15)
			if err != nil || record != maze/3 {
				t.Fatalf("dungeon%d maze%d record%d err%v", dungeon, maze, record, err)
			}
		}
	}
	for _, row := range []struct {
		d           uint32
		maze, count int
	}{{5054, -1, 15}, {5054, 15, 15}, {5054, 0, 14}, {210, 0, 15}} {
		if _, err := c.RecordForMaze(row.d, row.maze, row.count); err == nil {
			t.Fatal("invalid map accepted", row)
		}
	}
	cloned := c.Clone()
	cloned.Records[0][0] = 99
	if c.Records[0][0] != 0 {
		t.Fatal("resource clone aliased")
	}
}
