package worldmap

import (
	"fmt"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// NPCEnterTownLanding validates the actual NPC connection, rather than treating
// a nonzero op36 NPC key as a generic permission to cross town boundaries.
// Coordinates match the current 036FF950 producer: destination NPC X, Y+30.
func NPCEnterTownLanding(source dnfpvf.Source, sourceTownID int64, sourceMapPath, targetMapPath string, npcID, x, y int64) (bool, error) {
	landing, ok, err := ResolveNPCEnterTownLanding(source, sourceTownID, sourceMapPath, targetMapPath, npcID)
	return ok && landing.X == x && landing.Y == y, err
}

// ResolveNPCEnterTownLanding returns the authoritative destination after
// proving the visible source NPC's enter-town role and unique target spawn.
// Native NPC catalog objects need not retain the target map's spawn position.
func ResolveNPCEnterTownLanding(source dnfpvf.Source, sourceTownID int64, sourceMapPath, targetMapPath string, npcID int64) (Point, bool, error) {
	if source == nil || npcID <= 0 {
		return Point{}, false, nil
	}
	readMap := func(mapPath string) (string, *dnfpvf.Document, error) {
		resolved, doc, err := sourceDocument(source, DefaultMapList, mapPath)
		if err != nil {
			clean := strings.ToLower(strings.ReplaceAll(mapPath, "\\", "/"))
			resolved, doc, err = sourceDocument(source, DefaultMapList, clean)
			if err != nil && !strings.HasPrefix(path.Base(clean), "(r)") {
				// Runtime archives can retain only the revised basename, as do
				// existing drop and equipment document resolvers.
				return sourceDocument(source, DefaultMapList, path.Join(path.Dir(clean), "(r)"+path.Base(clean)))
			}
		}
		return resolved, doc, err
	}
	loadMap := func(mapPath string) (Map, error) {
		resolved, doc, err := readMap(mapPath)
		if err != nil {
			return Map{}, err
		}
		return parseMapWithImports(0, resolved, doc, func(candidate string) (string, *dnfpvf.Document, error) {
			return readMap(candidate)
		})
	}
	target, err := loadMap(targetMapPath)
	if err != nil {
		return Point{}, false, err
	}
	matches := 0
	var landing Point
	for _, npc := range target.NPCs {
		if npc.NPCID == npcID {
			matches++
			landing = Point{X: npc.Position.X, Y: npc.Position.Y + 30}
		}
	}
	if matches != 1 {
		return Point{}, false, nil
	}
	current, err := loadMap(sourceMapPath)
	if err != nil {
		return Point{}, false, err
	}
	list, err := sourceList(source, "npc/npc.lst")
	if err != nil {
		return Point{}, false, err
	}
	refs := make(map[int64]string, len(list))
	for _, entry := range list {
		if _, exists := refs[entry.ID]; exists {
			return Point{}, false, fmt.Errorf("duplicate npc id %d", entry.ID)
		}
		refs[entry.ID] = entry.Path
	}
	for _, npc := range current.NPCs {
		ref, ok := refs[npc.NPCID]
		if !ok {
			continue
		}
		_, doc, err := sourceDocument(source, "npc/npc.lst", ref)
		if err != nil {
			return Point{}, false, err
		}
		hidden := false
		for _, town := range doc.Ints("hide at town") {
			if town == sourceTownID {
				hidden = true
			}
		}
		if hidden {
			continue
		}
		role, ok := doc.Section("role")
		if !ok {
			continue
		}
		for i := 0; i+1 < len(role); i++ {
			if role[i].Kind == dnfpvf.TokenString && strings.EqualFold(strings.TrimSpace(role[i].Value), "[enter town]") && role[i+1].Kind == dnfpvf.TokenInt && role[i+1].Int == npcID {
				return landing, true, nil
			}
		}
	}
	return Point{}, false, nil
}
