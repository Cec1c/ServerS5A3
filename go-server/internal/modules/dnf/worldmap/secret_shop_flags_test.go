package worldmap

import "testing"

func TestDungeonSecretShopExclusionFlagsAfterMaze(t *testing.T) {
	for _, flag := range []string{"hidden secret shop", "dimension dungeon", "legend dungeon", "warriors dungeon"} {
		t.Run(flag, func(t *testing.T) {
			payload := ""
			if flag == "warriors dungeon" {
				payload = "\n`solo`"
			}
			doc := parseTestDocument(t, "dungeon/shop.dgn", "[maze info]\n[size]\n1 1\n["+flag+"]"+payload+"\n")
			d := ParseDungeon(1, "dungeon/shop.dgn", doc)
			if !d.Metadata.Flags[flag] {
				t.Fatalf("dungeon exclusion lost after maze: %s", flag)
			}
		})
	}
}
