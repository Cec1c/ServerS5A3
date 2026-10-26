package dnfbridge

import (
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var currentRaidReturnLocationFields = [...]string{"town_id", "area_id", "pos_x", "pos_y", "direction", "area_state"}

const currentRaidReturnOriginMarker = "raid_return_origin_v1"
const currentFiendWarReturnOriginMarker = "fiendwar_return_origin_v1"

func currentClearRaidReturnOrigin(stats map[string]int64) bool {
	changed := false
	for _, field := range currentRaidReturnLocationFields {
		key := "raid_return_" + field
		if _, exists := stats[key]; exists {
			delete(stats, key)
			changed = true
		}
	}
	for _, marker := range []string{currentRaidReturnOriginMarker, currentFiendWarReturnOriginMarker} {
		if _, exists := stats[marker]; exists {
			delete(stats, marker)
			changed = true
		}
	}
	return changed
}

// 只保存已提交且经PVF验证的普通落点；跨特殊频道不会覆盖首次进场原位。
func (s *Service) currentSaveRaidReturnOrigin(characterID uint16, character *dnfrepo.CharacterRecord) error {
	if character == nil || s.currentDedicatedChannelTown(character.Stats["town_id"]) {
		return nil
	}
	if _, found, reason := s.currentPersistedTownLoginLocation(characterID, *character); !found {
		return fmt.Errorf("团本入场原位不可用：%s", reason)
	}
	for _, field := range currentRaidReturnLocationFields {
		character.Stats["raid_return_"+field] = character.Stats[field]
	}
	character.Stats[currentRaidReturnOriginMarker] = 1
	return nil
}

func (s *Service) currentRaidReturnOriginLocation(characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, bool, error) {
	if character.Stats[currentRaidReturnOriginMarker] != 1 {
		return currentTownLoginLocation{}, false, nil
	}
	projected := dnfrepo.CloneCharacter(character)
	for _, field := range currentRaidReturnLocationFields {
		value, exists := character.Stats["raid_return_"+field]
		if !exists {
			return currentTownLoginLocation{}, false, fmt.Errorf("团本返城原位缺少字段%s", field)
		}
		projected.Stats[field] = value
	}
	if s.currentDedicatedChannelTown(projected.Stats["town_id"]) {
		return currentTownLoginLocation{}, false, fmt.Errorf("团本返城原位不是普通城镇")
	}
	location, found, reason := s.currentPersistedTownLoginLocation(characterID, projected)
	if !found {
		return currentTownLoginLocation{}, false, fmt.Errorf("团本返城原位不可用：%s", reason)
	}
	location.Branch, location.Source, location.PositionSource = "团本返回入场前原位", "角色已保存的团本入场原位", "入场前实际城镇坐标"
	return location, true, nil
}

func (s *Service) currentRaidExitLoginLocation(characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, error) {
	location, found, err := s.currentRaidReturnOriginLocation(characterID, character)
	if err != nil || found {
		return location, err
	}
	// 决斗直接转入团本时，继续保留此前普通城镇的有效来源。
	location, found, err = s.currentPVPReturnOriginLocation(characterID, character)
	if err != nil || found {
		return location, err
	}
	location, err = s.currentInitialTownSeriaLoginLocation()
	location.Branch, location.Source = "旧团本记录缺少入场原位", "真实PVF房间兼容回落"
	return location, err
}
