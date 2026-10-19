package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Cold selection and manual channel reconnect must enter the same PVF room.
// The ordinary login fallback must not replace a raid channel's registered room.
func currentRaidLoginTownID(session *gameSession) (int64, bool) {
	if session == nil {
		return 0, false
	}
	switch session.channel.Type {
	case dnfenum.RaidChannelType:
		return 19, true
	case dnfenum.HiddenRaidType:
		return 32, true
	default:
		return 0, false
	}
}

func (s *Service) currentCharacterListLoginLocationForSession(session *gameSession, characterID uint16, character dnfrepo.CharacterRecord) (currentTownLoginLocation, int64, string, string, error) {
	townID, raidChannel := currentRaidLoginTownID(session)
	if !raidChannel {
		if currentLoginLeavingChannelTown(session, character) && character.Stats["town_id"] == 10 {
			location, err := s.currentPVPExitLoginLocation(characterID, character)
			return location, character.Stats["town_id"], "", "", err
		}
		if currentLoginLeavingChannelTown(session, character) {
			location, err := s.currentRaidExitLoginLocation(characterID, character)
			if err != nil {
				return currentTownLoginLocation{}, character.Stats["town_id"], "", "", err
			}
			location.Branch = "ordinary_channel_after_raid"
			reason := "raid_town_not_valid_for_ordinary_channel"
			if character.Stats["town_id"] == 10 {
				location.Branch = "普通频道退出决斗场"
				reason = "决斗城镇不可作为普通频道落点"
			}
			location.Source = "ordinary_channel_runtime_pvf_seria_room"
			return location, character.Stats["town_id"], reason, "", nil
		}
		return s.currentCharacterListLoginLocation(characterID, character)
	}
	location, found, reason := s.currentTownSeriaRoomLoginLocation(townID)
	if !found {
		return currentTownLoginLocation{}, -1, "", "", fmt.Errorf("raid login room unavailable: %s", reason)
	}
	persistedTownID := int64(-1)
	if saved, found, _ := currentPersistedTownLoginTownID(character); found {
		persistedTownID = saved
	}
	location.Branch = "raid_channel_seria_room"
	location.Source = "raid_channel_runtime_pvf_seria_room"
	return location, persistedTownID, "", "", nil
}

func currentLoginLeavingChannelTown(session *gameSession, character dnfrepo.CharacterRecord) bool {
	if session == nil {
		return false
	}
	if _, raidChannel := currentRaidLoginTownID(session); raidChannel || isPVPChannel(session.channel) {
		return false
	}
	return currentRegisteredChannelTown(character.Stats["town_id"])
}

func currentRegisteredChannelTown(townID int64) bool {
	// 专用城镇不能作为普通频道落点或覆盖普通城镇的返城原位。
	return townID == 10 || townID == 19 || townID == 32
}

func (s *Service) currentSessionSelectAckTownID(session *gameSession, character dnfrepo.CharacterRecord, characterID uint16, slot int) (uint8, string, error) {
	if _, raidChannel := currentRaidLoginTownID(session); raidChannel {
		location, _, _, _, err := s.currentCharacterListLoginLocationForSession(session, characterID, character)
		if err != nil {
			return 0, "", err
		}
		return location.TownID, location.Source, nil
	}
	townID, source, ok := currentSelectAckTownID(slot)
	if !ok {
		return 0, source, fmt.Errorf("current select ACK town outside u8")
	}
	return townID, source, nil
}

// 保留上游既有入口供其它普通登录调用；专用城镇判定统一收口。
func currentLoginLeavingRaidTown(session *gameSession, character dnfrepo.CharacterRecord) bool {
	return currentLoginLeavingChannelTown(session, character)
}
