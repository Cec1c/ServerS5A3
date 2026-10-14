package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/expertjob"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func (s *Service) validateCurrentExpertJobStorePlacement(player onlinePlayerInfo, request currentExpertJobStoreCreateRequest) error {
	area, found := s.townArea(int64(player.TownID), int64(player.AreaID))
	if !found {
		return expertjob.ErrStorePointUnavailable
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil || catalog.source == nil {
		return expertjob.ErrStorePointUnavailable
	}
	m, err := worldmap.LoadTownPlacementMap(catalog.source, area.MapPath)
	if err != nil {
		return fmt.Errorf("%w: %v", expertjob.ErrStorePointUnavailable, err)
	}
	return expertjob.ValidateStorePlacement(m, int64(request.PositionX), int64(request.PositionY))
}
