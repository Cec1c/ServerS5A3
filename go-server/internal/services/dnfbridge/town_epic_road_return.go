package dnfbridge

import (
	"strings"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// The actual 2018 Epic Road town excludes user pretown info but omits disable
// save point. It is an excursion from Seria, not the character's ordinary
// return town. Bind this exception to the complete published resource identity;
// neither a town number nor an event-like name alone authorizes it.
func currentTownIsEpicRoadReturnOrigin(town dnftown.Town) bool {
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
