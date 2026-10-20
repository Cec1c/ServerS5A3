package main

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type lukeAlias struct {
	file                   string
	dungeon, originalOwner int64
	room, rooms            int
}

// The DGN's explicit IDs and the matching family/room-numbered MAP files are
// reviewed together. These aliases only repair the list, never asset contents.
func lukeSingleAliases() map[int64]lukeAlias {
	type family struct {
		first, dungeon int64
		name, prefix   string
		rooms          int
	}
	families := []family{
		{95267, 5323, "sanctum_of_birth", "sanctum_of_birth_l", 5},
		{95272, 5324, "sanctum_of_extinction", "sanctum_of_extinction", 7},
		{95279, 5325, "altar_of_light", "altar_of_light", 5},
		{95284, 5326, "altar_of_force", "altar_of_force", 5},
		{95289, 5327, "light_temple", "light_temple", 5},
		{95294, 5328, "solium_machina", "solium_machina_l", 2},
		{95296, 5329, "sanctum_of_birth", "sanctum_of_birth_d", 5},
		{95301, 5330, "sanctum_of_spread", "sanctum_of_spread", 7},
		{95308, 5331, "altar_of_dark", "altar_of_dark", 5},
		{95313, 5332, "altar_of_wisdom", "altar_of_wisdom", 5},
		{95318, 5333, "dark_temple", "dark_temple", 5},
		{95323, 5334, "solium_machina", "solium_machina_d", 2},
	}
	result := map[int64]lukeAlias{}
	for _, f := range families {
		for room := 1; room <= f.rooms; room++ {
			id := f.first + int64(room-1)
			owner := f.dungeon
			// The shipped spread boss retains its normal-raid owner. Explicit
			// DGN specifications have precedence over MAP ownership in the
			// established resolver. Preserve this metadata instead of editing it.
			if id == 95307 {
				owner = 5056
			}
			result[id] = lukeAlias{fmt.Sprintf("map/luke_single/%s/%s%d.map", f.name, f.prefix, room), f.dungeon, owner, room, f.rooms}
		}
	}
	return result
}

func validateLukeAlias(id int64, alias lukeAlias, doc *dnfpvf.Document) error {
	m := worldmap.ParseMap(id, alias.file, doc)
	if !m.DungeonID.Set || m.DungeonID.Value != alias.originalOwner {
		return fmt.Errorf("Luke map %d original owner mismatch", id)
	}
	if !m.PathgateRecognizeRange.Set || len(doc.Ints("pathgate recognize range")) != 8 || len(m.PathgatePositions) != 4 {
		return fmt.Errorf("Luke map %d native door metadata missing", id)
	}
	if alias.room == 1 && (len(doc.Ints("dungeon start area")) != 4 || !strings.Contains(m.Greed, "BB")) {
		return fmt.Errorf("Luke map %d start role mismatch", id)
	}
	if alias.room == alias.rooms && !strings.Contains(m.Greed, "EE") {
		return fmt.Errorf("Luke map %d boss role mismatch", id)
	}
	return nil
}
