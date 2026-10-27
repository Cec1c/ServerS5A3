package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentInitialTownTransition(
	ctx context.Context,
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	if isCurrentFiendWarChannel(session) {
		return s.currentRaidEntranceTownTransition(session, characterID, character)
	}
	if s.isCurrentTagTournamentChannel(session) {
		return s.currentTagTournamentTownTransition(characterID, character)
	}
	if session != nil && isPVPChannel(session.channel) {
		return s.currentPVPTownTransition(session, characterID, character)
	}
	return s.currentPersistedTownTransition(ctx, session, characterID, character, "selected_character_persisted_location_read_only")
}

func (s *Service) currentChannelReconnectTownTransition(
	ctx context.Context,
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	if s.isCurrentTagTournamentChannel(session) {
		return s.currentTagTournamentTownTransition(characterID, character)
	}
	if session != nil && isPVPChannel(session.channel) {
		return s.currentPVPTownTransition(session, characterID, character)
	}
	if session != nil && (session.channel.Type == dnfenum.RaidChannelType || session.channel.Type == dnfenum.HiddenRaidType || session.channel.Type == 42) {
		return s.currentRaidEntranceTownTransition(session, characterID, character)
	}
	// 所有普通频道入口共用选角色的房间政策，包含从决斗、无双及团本退回。
	// 入场前的普通位置用于确定所属城镇和房门，不能直接用作街道出生点。
	if session == nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("普通换线缺少角色会话")
	}
	session.townMu.Lock()
	defer session.townMu.Unlock()
	return s.currentCharacterListLoginTransition(ctx, session, characterID, character)
}

// Raid channel entry uses the town's Seria room, not its dungeon gate.
// Both PVF area-zero maps import Common/(R)Gate_Seria.map and declare a gate.
func (s *Service) currentRaidEntranceTownTransition(session *gameSession, characterID uint16, character dnfrepo.CharacterRecord) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	if isCurrentFiendWarChannel(session) {
		loc, err := s.currentFiendWarLoginLocation(session, characterID)
		if err != nil {
			return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
		}
		return currentReconnectLocationProjection(characterID, character, loc)
	}
	townID := int64(19)
	if session.channel.Type == dnfenum.HiddenRaidType {
		townID = 32
	}
	location, found, reason := s.currentTownSeriaRoomLoginLocation(townID)
	if !found {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf("raid Seria room unavailable: %s", reason)
	}
	s.logGameEvent(session, "game-raid-seria-location-used", "town_id", townID, "area_id", location.AreaID, "map_path", location.MapPath, "position_x", location.PositionX, "position_y", location.PositionY, "source", location.PositionSource)
	return currentReconnectLocationProjection(characterID, character, location)
}

func currentReconnectLocationProjection(characterID uint16, character dnfrepo.CharacterRecord, location currentTownLoginLocation) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	projected := dnfrepo.CloneCharacter(character)
	if projected.Stats == nil {
		projected.Stats = make(map[string]int64)
	}
	projected.Stats["town_id"], projected.Stats["area_id"] = int64(location.TownID), int64(location.AreaID)
	projected.Stats["pos_x"], projected.Stats["pos_y"] = location.PositionX, location.PositionY
	projected.Stats["direction"], projected.Stats["area_state"] = int64(location.Direction), int64(location.AreaState)
	row := currentSceneTransitionRow{ObjectOrResourceKey: currentSceneActorObjectKey(characterID), Value1: uint16(int16(location.PositionX)), Value2: uint16(int16(location.PositionY)), Value3: location.Direction, Value4: location.AreaState}
	return projected, location.TownID, location.AreaID, row, location.MapPath, nil
}

func (s *Service) currentPersistedTownTransition(
	ctx context.Context,
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
	locationSource string,
) (dnfrepo.CharacterRecord, byte, byte, currentSceneTransitionRow, string, error) {
	townID, err := requiredCurrentInitialTownU8(character, "town_id")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	areaID, err := requiredCurrentInitialTownU8(character, "area_id")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	positionX, err := requiredCurrentInitialTownCoordinate(character, "pos_x")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	positionY, err := requiredCurrentInitialTownCoordinate(character, "pos_y")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	direction, err := requiredCurrentInitialTownU8(character, "direction")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	areaState, err := requiredCurrentInitialTownU8(character, "area_state")
	if err != nil {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", err
	}
	area, found := s.townArea(int64(townID), int64(areaID))
	if !found || area.MapPath == "" {
		return dnfrepo.CharacterRecord{}, 0, 0, currentSceneTransitionRow{}, "", fmt.Errorf(
			"persisted town %d area %d has no runtime PVF map path",
			townID, areaID,
		)
	}
	if locationSource == "" {
		locationSource = "persisted_location_read_only"
	}
	s.logGameEvent(session, "game-initial-town-persisted-location-used",
		"char_id", characterID,
		"town_id", townID,
		"area_id", areaID,
		"position_x", int16(positionX),
		"position_y", int16(positionY),
		"direction", direction,
		"area_state", areaState,
		"map_path", area.MapPath,
		"need_quest_unlock_allowed", false,
		"source", locationSource)
	return character, townID, areaID, currentSceneTransitionRow{
		ObjectOrResourceKey: currentSceneActorObjectKey(characterID),
		Value1:              positionX,
		Value2:              positionY,
		Value3:              direction,
		Value4:              areaState,
	}, area.MapPath, nil
}

func requiredCurrentInitialTownCoordinate(character dnfrepo.CharacterRecord, key string) (uint16, error) {
	if character.Stats == nil {
		return 0, fmt.Errorf("selected character stats not loaded")
	}
	value, found := character.Stats[key]
	if !found {
		return 0, fmt.Errorf("selected character %s not loaded", key)
	}
	if value < -1<<15 || value > 1<<15-1 {
		return 0, fmt.Errorf("selected character %s %d is outside i16", key, value)
	}
	return uint16(int16(value)), nil
}

func requiredCurrentInitialTownU8(character dnfrepo.CharacterRecord, key string) (byte, error) {
	if character.Stats == nil {
		return 0, fmt.Errorf("selected character stats not loaded")
	}
	value, found := character.Stats[key]
	if !found {
		return 0, fmt.Errorf("selected character %s not loaded", key)
	}
	if value < 0 || value > int64(^byte(0)) {
		return 0, fmt.Errorf("selected character %s %d is outside u8", key, value)
	}
	return byte(value), nil
}
