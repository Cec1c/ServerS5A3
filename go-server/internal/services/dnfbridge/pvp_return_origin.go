package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentPVPReturnOriginMarker = "pvp_return_origin_v1"

var currentPVPReturnLocationFields = [...]string{"town_id", "area_id", "pos_x", "pos_y", "direction", "area_state"}

func currentClearPVPReturnOrigin(stats map[string]int64) bool {
	changed := false
	for _, field := range append([]string{currentPVPReturnOriginMarker}, currentPVPReturnLocationFields[:]...) {
		key := field
		if field != currentPVPReturnOriginMarker {
			key = "pvp_return_" + field
		}
		if _, exists := stats[key]; exists {
			delete(stats, key)
			changed = true
		}
	}
	return changed
}

// 原位只取本角色已提交的普通城镇，地图和坐标边界仍经现有PVF解析校验。
func (s *Service) currentPVPReturnOriginLocation(characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, bool, error) {
	if character.Stats[currentPVPReturnOriginMarker] != 1 {
		return currentTownLoginLocation{}, false, nil
	}
	projected := dnfrepo.CloneCharacter(character)
	for _, field := range currentPVPReturnLocationFields {
		value, exists := character.Stats["pvp_return_"+field]
		if !exists {
			return currentTownLoginLocation{}, false, fmt.Errorf("决斗返城原位缺少字段%s", field)
		}
		projected.Stats[field] = value
	}
	if currentRegisteredChannelTown(projected.Stats["town_id"]) {
		return currentTownLoginLocation{}, false, fmt.Errorf("决斗返城原位不是普通城镇")
	}
	location, found, reason := s.currentPersistedTownLoginLocation(characterID, projected)
	if !found {
		return currentTownLoginLocation{}, false, fmt.Errorf("决斗返城原位不可用：%s", reason)
	}
	location.Branch, location.Source, location.PositionSource = "决斗返回入场前原位", "角色已保存的决斗入场原位", "入场前实际城镇坐标"
	return location, true, nil
}

func (s *Service) currentPVPExitLoginLocation(characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, error) {
	location, found, err := s.currentPVPReturnOriginLocation(characterID, character)
	if err != nil || found {
		return location, err
	}
	location, found, err = s.currentRaidReturnOriginLocation(characterID, character)
	if err != nil || found {
		return location, err
	}
	// 旧版本没有保存入场原位；只沿用已有PVF房间回落，不猜测历史坐标。
	location, err = s.currentInitialTownSeriaLoginLocation()
	location.Branch, location.Source = "旧决斗记录缺少入场原位", "真实PVF房间兼容回落"
	return location, err
}

func (s *Service) currentSavePVPReturnOrigin(characterID uint16, character *dnfrepo.CharacterRecord) error {
	if character == nil || currentRegisteredChannelTown(character.Stats["town_id"]) {
		return nil
	}
	if _, found, reason := s.currentPersistedTownLoginLocation(characterID, *character); !found {
		return fmt.Errorf("决斗入场原位不可用：%s", reason)
	}
	for _, field := range currentPVPReturnLocationFields {
		character.Stats["pvp_return_"+field] = character.Stats[field]
	}
	character.Stats[currentPVPReturnOriginMarker] = 1
	return nil
}

// 高频位移仅在会话内保存；旧频道退出ACK之前冻结最后一个有效op35位置。
func (s *Service) captureCurrentOrdinaryChannelExitPosition(session *gameSession) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 || session.accountID == "" || isPVPChannel(session.channel) {
		return nil
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		return nil
	}
	session.townMu.Lock()
	snapshot, ready := session.townPositionSnapshot, session.townSceneReadyCharacterID
	session.townMu.Unlock()
	if ready != session.selectedCharacterID || snapshot.CharacterID != ready || !snapshot.PositionValid || currentRegisteredChannelTown(int64(snapshot.TownID)) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return fmt.Errorf("退出频道时角色仓储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(ready)))
	if err != nil {
		return err
	}
	if !found || c.AccountID != session.accountID {
		return fmt.Errorf("退出频道的原位角色归属不匹配")
	}
	if c.Stats["town_id"] != int64(snapshot.TownID) || c.Stats["area_id"] != int64(snapshot.AreaID) {
		return nil
	}
	c = dnfrepo.CloneCharacter(c)
	c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"] = int64(int16(snapshot.PositionX)), int64(int16(snapshot.PositionY)), int64(snapshot.MovementCode)
	if _, found, reason := s.currentPersistedTownLoginLocation(ready, c); !found {
		return fmt.Errorf("退出频道时原位无效：%s", reason)
	}
	_, err = s.commitCurrentReconnectLocation(ctx, ready, c)
	return err
}
