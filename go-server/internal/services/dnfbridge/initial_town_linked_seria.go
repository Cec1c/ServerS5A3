package dnfbridge

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

// 当前PVF的多个普通城镇共用38/1房间地图。按来源区域优先查真实房门，
// 房间地图编号不能代替其所属城镇；所属城镇仍由prev-village和1375保存。
// 某些资源有自己的房间，必须跟随其门的目标，不能一律回落到防线。
func (s *Service) currentTownLinkedSeriaLoginLocation(characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, bool) {
	if _, valid, _ := s.currentPersistedTownLoginLocation(characterID, character); !valid {
		return currentTownLoginLocation{}, false
	}
	townID, areaID := character.Stats["town_id"], character.Stats["area_id"]
	if s.currentDedicatedChannelTown(townID) {
		return currentTownLoginLocation{}, false
	}
	areas, found := s.townAreas(townID)
	if !found {
		return currentTownLoginLocation{}, false
	}
	readMap, ok, _ := s.currentSeriaRoomDoorMapReader()
	if !ok {
		return currentTownLoginLocation{}, false
	}
	for _, area := range orderSeriaRoomDoorCandidateAreas(areas, areaID) {
		if townMapPathLooksLikeSeria(area.MapPath) {
			continue
		}
		parsed, ok := readMap(area)
		if !ok {
			continue
		}
		for _, door := range currentSeriaRoomDoorTriggersInMapOrder(parsed) {
			if door.TargetTownID < 0 || door.TargetTownID > 255 || s.currentDedicatedChannelTown(door.TargetTownID) {
				continue
			}
			location, found, _ := s.currentTownSeriaRoomLoginLocation(door.TargetTownID)
			if found && int64(location.AreaID) == door.TargetAreaID {
				location.Source = "runtime_pvf_entry_town_linked_seria_room"
				return location, true
			}
		}
	}
	return currentTownLoginLocation{}, false
}
