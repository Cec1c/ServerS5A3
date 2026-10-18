package imagecommunication

import (
	"context"
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Scene is a PVF-derived source-menu location, separate from remote targets.
// The bodyless command cannot identify which source NPC was clicked.
type Scene struct {
	TownID     int64
	Area       town.Area
	SourceNPCs []int64
}

func LoadScene(ctx context.Context, source dnfpvf.Source, towns *town.Table, townID, areaID int64) (Scene, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil || towns == nil {
		return Scene{}, ErrSourceRequired
	}
	area, found := towns.FindArea(townID, areaID)
	if !found {
		return Scene{}, ErrScene
	}
	m, err := worldmap.LoadTownPlacementMap(source, area.MapPath)
	if err != nil {
		return Scene{}, fmt.Errorf("image communication source map: %w", err)
	}
	list, err := source.ReadText(NPCListPath)
	if err != nil {
		return Scene{}, err
	}
	doc, err := dnfpvf.Parse(NPCListPath, list)
	if err != nil {
		return Scene{}, err
	}
	refs := map[int64]string{}
	for _, e := range dnfpvf.ParseList(doc) {
		if _, exists := refs[e.ID]; exists {
			return Scene{}, ErrDuplicateNPC
		}
		refs[e.ID] = resolveNPCReference(e.Path)
	}
	scene := Scene{TownID: townID, Area: area}
	seen := map[int64]bool{}
	for _, npc := range m.NPCs {
		if err := ctx.Err(); err != nil {
			return Scene{}, err
		}
		ref, exists := refs[npc.NPCID]
		if !exists {
			return Scene{}, fmt.Errorf("%w: source NPC %d", ErrMissingNPC, npc.NPCID)
		}
		text, err := source.ReadText(ref)
		if err != nil {
			return Scene{}, err
		}
		def, err := dnfpvf.Parse(ref, text)
		if err != nil {
			return Scene{}, err
		}
		hidden := false
		for _, id := range def.Ints("hide at town") {
			hidden = hidden || id == townID
		}
		if hidden {
			continue
		}
		role, _ := def.Section("role")
		for i := 0; i+1 < len(role); i++ {
			if role[i].Kind == dnfpvf.TokenString && strings.EqualFold(strings.TrimSpace(role[i].Value), "[image communication]") && role[i+1].Kind == dnfpvf.TokenInt && role[i+1].Int == 0 && !seen[npc.NPCID] {
				scene.SourceNPCs = append(scene.SourceNPCs, npc.NPCID)
				seen[npc.NPCID] = true
			}
		}
	}
	if len(scene.SourceNPCs) == 0 {
		return Scene{}, ErrScene
	}
	return scene, nil
}
