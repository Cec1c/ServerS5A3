package worldmap

import (
	"context"
	"testing"
)

func TestSupplementalDungeonIndexUsesExplicitIDsRootAndPrimaryOwnership(t *testing.T) {
	source := textSource{
		"dungeon/dungeon.lst":                    "210 `anton/normal.dgn`",
		"dungeon/anton/normal.dgn":               "[name]\n`normal`\n",
		"dungeon/tutorial/dungeon.lst":           "210 `missing/foreign.dgn` 5314 `Anton_New/Close_Blacksmoke.dgn` 9999 `missing/obsolete.dgn`",
		"dungeon/Anton_New/Close_Blacksmoke.dgn": "[name]\n`single`\n",
	}
	options := Options{SkipMaps: true, SkipAreas: true, SupplementalDungeonListPath: "dungeon/tutorial/dungeon.lst", SupplementalDungeonReferenceRoot: "dungeon", SupplementalDungeonIDs: []int64{210, 5314}}
	table, err := LoadSource(context.Background(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	primary, ok := table.FindDungeon(210)
	if !ok || primary.Path != "dungeon/anton/normal.dgn" {
		t.Fatal("secondary index replaced primary ownership", primary)
	}
	single, ok := table.FindDungeon(5314)
	if !ok || single.Path != "dungeon/Anton_New/Close_Blacksmoke.dgn" {
		t.Fatal("secondary reference root was guessed", single)
	}
	if _, ok := table.FindDungeon(9999); ok || len(table.Dungeons()) != 2 {
		t.Fatal("unrequested tutorial data entered gameplay")
	}
	for _, name := range []string{"missing_id", "unreadable_document", "duplicate_id", "zero_id", "missing_list"} {
		t.Run(name, func(t *testing.T) {
			copySource := textSource{}
			for k, v := range source {
				copySource[k] = v
			}
			changed := options
			switch name {
			case "missing_id":
				changed.SupplementalDungeonIDs = []int64{5315}
			case "unreadable_document":
				delete(copySource, "dungeon/Anton_New/Close_Blacksmoke.dgn")
			case "duplicate_id":
				copySource[options.SupplementalDungeonListPath] += " 5314 `different.dgn`"
			case "zero_id":
				changed.SupplementalDungeonIDs = []int64{0}
			case "missing_list":
				changed.SupplementalDungeonListPath = ""
			}
			if table, err := LoadSource(context.Background(), copySource, changed); err == nil || table != nil {
				t.Fatal("invalid supplemental dependency partially published", err)
			}
		})
	}
}
