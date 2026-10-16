package dnfbridge

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The native op25 ID source carries the menu recipe, not an owned drawing.
// Authorize it through the ready scene's NPC -> product-item shop -> recipe
// chain. A normal item-shop listing alone does not authorize production.
func (s *Service) currentNPCProductionSource(identity boundGameSessionCharacter, source dnfpvf.Source, request currentEquipmentRecipeCompoundRequest) (currentEquipmentRecipeCompoundSource, error) {
	refused := func() (currentEquipmentRecipeCompoundSource, error) {
		return currentEquipmentRecipeCompoundSource{}, errCurrentEquipmentRecipeCompoundSourceInvalid
	}
	if source == nil || !request.SourceIsItemID || request.SourceItemID <= 0 || request.Count <= 0 {
		return refused()
	}
	if !s.boundGameSessionCharacterCurrent(identity) || currentDungeonSceneActive(identity.session) {
		return refused()
	}
	session := identity.session
	session.townMu.Lock()
	position, ready := session.townPositionSnapshot, session.townSceneReadyCharacterID
	session.townMu.Unlock()
	if !position.PositionValid || position.CharacterID != identity.character || ready != identity.character {
		return refused()
	}
	area, ok := s.townArea(int64(position.TownID), int64(position.AreaID))
	if !ok {
		return refused()
	}
	m, err := worldmap.LoadTownPlacementMap(source, area.MapPath)
	if err != nil {
		return currentEquipmentRecipeCompoundSource{}, err
	}
	npcPaths, err := currentNPCProductionList(source, "npc/npc.lst")
	if err != nil {
		return currentEquipmentRecipeCompoundSource{}, err
	}
	shopPaths, err := currentNPCProductionList(source, currentNPCShopItemListPath)
	if err != nil {
		return currentEquipmentRecipeCompoundSource{}, err
	}
	for _, spawn := range m.NPCs {
		ref, listed := npcPaths[spawn.NPCID]
		if !listed {
			continue
		}
		npc, err := currentNPCProductionDocument(source, "npc/npc.lst", ref)
		if err != nil {
			return currentEquipmentRecipeCompoundSource{}, err
		}
		hidden := false
		for _, id := range npc.Ints("hide at town") {
			hidden = hidden || id == int64(position.TownID)
		}
		if hidden {
			continue
		}
		role, found, err := equipmentRecipeSectionTokens(npc, "role")
		if err != nil {
			return currentEquipmentRecipeCompoundSource{}, err
		}
		if !found {
			continue
		}
		for i, token := range role {
			if token.Kind != dnfpvf.TokenString || strings.ToLower(strings.TrimSpace(token.Value)) != "[product item]" {
				continue
			}
			if i+1 >= len(role) || role[i+1].Kind != dnfpvf.TokenInt || role[i+1].Int <= 0 {
				return refused()
			}
			shopRef, listed := shopPaths[role[i+1].Int]
			if !listed {
				return refused()
			}
			shop, err := currentNPCProductionDocument(source, currentNPCShopItemListPath, shopRef)
			if err != nil {
				return currentEquipmentRecipeCompoundSource{}, err
			}
			npcID, present, err := equipmentRecipeSectionTokens(shop, "NPC")
			if err != nil || !present || len(npcID) != 1 || npcID[0].Kind != dnfpvf.TokenInt || npcID[0].Int != spawn.NPCID {
				return refused()
			}
			allowed := false
			for _, section := range shop.Sections {
				if !strings.EqualFold(strings.TrimSpace(section.Name), "item list") {
					continue
				}
				if section.Start < 0 || section.End > len(shop.Tokens) || section.Start > section.End {
					return refused()
				}
				for _, item := range shop.Tokens[section.Start:section.End] {
					if item.Kind != dnfpvf.TokenInt || item.Int <= 0 {
						return refused()
					}
					if item.Int == request.SourceItemID {
						allowed = true
					}
				}
			}
			if allowed && s.boundGameSessionCharacterCurrent(identity) {
				return currentEquipmentRecipeCompoundSource{ItemID: request.SourceItemID, Slot: -1, Count: request.Count}, nil
			}
		}
	}
	return refused()
}

// Keep ID mappings single-valued instead of silently using the first duplicate.
func currentNPCProductionList(source dnfpvf.Source, listPath string) (map[int64]string, error) {
	text, err := source.ReadText(listPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(listPath, text)
	if err != nil {
		return nil, err
	}
	refs := make(map[int64]string)
	for _, entry := range dnfpvf.ParseList(doc) {
		if entry.ID <= 0 {
			continue
		}
		if _, duplicate := refs[entry.ID]; duplicate {
			return nil, fmt.Errorf("NPC production duplicate mapping in %s: %d", listPath, entry.ID)
		}
		refs[entry.ID] = entry.Path
	}
	return refs, nil
}

func currentNPCProductionDocument(source dnfpvf.Source, listPath, reference string) (*dnfpvf.Document, error) {
	resolved, err := resolveCurrentUpgradeNPCReference(listPath, reference)
	if err != nil {
		return nil, err
	}
	resolved, text, err := readDungeonDropText(source, "", resolved)
	if err != nil {
		return nil, err
	}
	return dnfpvf.Parse(resolved, text)
}
