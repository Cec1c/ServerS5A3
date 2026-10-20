package worldmap

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// LoadTownPlacementMap resolves a town's explicit map path, including imported
// NPC spawns. Incomplete collision/NPC data must not authorize a store position.
func LoadTownPlacementMap(source dnfpvf.Source, mapPath string) (Map, error) {
	if source == nil {
		return Map{}, ErrSourceRequired
	}
	load := func(reference string) (string, *dnfpvf.Document, error) {
		resolved, doc, err := sourceDocument(source, DefaultMapList, reference)
		if err != nil {
			resolved, doc, err = sourceDocument(source, DefaultMapList, strings.ToLower(reference))
		}
		if err != nil {
			return "", nil, err
		}
		parsed := ParseMap(0, resolved, doc)
		for _, d := range parsed.Diagnostics {
			if sectionKey(d.Section) == "npc" || sectionKey(d.Section) == "virtual movable area" {
				return "", nil, fmt.Errorf("invalid placement data in %s: %s", resolved, d.Message)
			}
		}
		return resolved, doc, nil
	}
	resolved, doc, err := load(mapPath)
	if err != nil {
		return Map{}, err
	}
	return parseMapWithImports(0, resolved, doc, load)
}
