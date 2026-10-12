package dnfbridge

import (
	"strings"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// The published 2016 and 2018 Epic Road towns omit disable save point. They
// are excursions, not the character's ordinary return town. Bind this
// exception to the complete published resource identity;
// neither a town number nor an event-like name alone authorizes it.
func currentTownIsEpicRoadReturnOrigin(town dnftown.Town) bool {
	if town.ID == 28 && strings.EqualFold(cleanInitialPVFPath(town.Path), "town/event_hendon.twn") && len(town.Areas) == 2 {
		var street, room bool
		for _, area := range town.Areas {
			street = street || area.ID == 0 && strings.EqualFold(cleanInitialPVFPath(area.MapPath), "event/16_epicroad_town/16_epicroad_town.map") && area.DungeonGate != nil && *area.DungeonGate == 35
			room = room || area.ID == 1 && strings.EqualFold(cleanInitialPVFPath(area.MapPath), "event/16_epicroad_town/(r)gate_16_epicroad_town.map") && area.Gate != nil && area.Gate.X == 474 && area.Gate.Y == 234
		}
		return street && room
	}
	if town.ID != 50 || !strings.EqualFold(cleanInitialPVFPath(town.Path), "town/2018_epicroad2.twn") || len(town.Areas) != 1 {
		return false
	}
	area := town.Areas[0]
	return area.ID == 0 && strings.EqualFold(cleanInitialPVFPath(area.MapPath), "event/2018_epicroad2_town/2018_epicroad2_town.map") &&
		area.DungeonGate != nil && *area.DungeonGate == 47
}

func (s *Service) currentTownEpicRoadReturnOrigin(origin currentTownPositionSnapshot) bool {
	s.townCatalogMu.RLock()
	table := s.townCatalog
	s.townCatalogMu.RUnlock()
	if table == nil {
		return false
	}
	town, found := table.Find(int64(origin.TownID))
	return found && currentTownIsEpicRoadReturnOrigin(town)
}
