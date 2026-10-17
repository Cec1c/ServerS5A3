package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Require the conversion NPC to belong to the character's ready town scene.
// This proves scene membership, not an interaction radius; no distance is
// invented without current-client evidence. A rejection never changes payment
// to gold silently. The whole payment candidate remains explicitly opt-in.
func (s *Service) validateUpgradeNPCScene(identity boundGameSessionCharacter, npcID uint16) error {
	if !s.boundGameSessionCharacterCurrent(identity) {
		return fmt.Errorf("upgrade NPC scene unavailable")
	}
	session := identity.session
	session.townMu.Lock()
	position := session.townPositionSnapshot
	readyID := session.townSceneReadyCharacterID
	session.townMu.Unlock()
	if !position.PositionValid || position.CharacterID == 0 || position.CharacterID != readyID || position.CharacterID != identity.character || currentDungeonSceneActive(session) {
		return fmt.Errorf("upgrade NPC town scene not ready")
	}
	area, found := s.townArea(int64(position.TownID), int64(position.AreaID))
	if !found {
		return fmt.Errorf("upgrade NPC town area unavailable")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	m, err := worldmap.LoadTownPlacementMap(catalog.source, area.MapPath)
	if err != nil {
		return err
	}
	for _, npc := range m.NPCs {
		if npc.NPCID == int64(npcID) {
			if !s.boundGameSessionCharacterCurrent(identity) {
				return fmt.Errorf("upgrade NPC identity changed")
			}
			return nil
		}
	}
	return fmt.Errorf("upgrade NPC %d not in current town area", npcID)
}
