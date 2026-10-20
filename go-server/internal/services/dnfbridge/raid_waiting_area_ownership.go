package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// The formal 369d town19/area2 map and gate27, also observed in current
// client op36, identify the Anton entrance. Main deck and Seria stay separate.
// Retain this existing hook name for town/raid callers; the exact Luke program
// waiting room shares the same admission, presence and removal lifecycle.
func (s *Service) currentAntonWaitingArea(townID, areaID byte) bool {
	if s.currentLukeWaitingArea(townID, areaID) {
		return true
	}
	if s == nil || s.options.currentClientVersion != "official-24.9" || townID != 19 || areaID != 2 {
		return false
	}
	area, found := s.townArea(int64(townID), int64(areaID))
	return found && strings.EqualFold(strings.ReplaceAll(area.MapPath, "\\", "/"), "Town/BattleShip/BattleShip_Anton.map") && area.DungeonGate != nil && *area.DungeonGate == 27
}

// Caller holds raidMu only while taking a scope snapshot or committing presence.
// Release before repository work and all network/store/party projection calls.
// Final publication revalidates membership; removal uses the same short lock.
func (s *Service) lockCurrentAntonWaitingAreaScope(session *gameSession, townID, areaID byte) (uint32, string, bool, func()) {
	noop := func() {}
	if !s.currentAntonWaitingArea(townID, areaID) {
		return 0, "", true, noop
	}
	channelType, route := byte(23), byte(0)
	if s.currentLukeWaitingArea(townID, areaID) {
		channelType, route = 32, 1
	}
	if !s.currentRaidAuthenticated(session) || session.channel.Type != channelType || session.channel.ID <= 0 || session.channel.ID > 255 {
		return 0, "", false, noop
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.CurrentLayout || state.RaidKey == 0 || state.RouteOrRaidType != route || byte(state.RaidKey>>16) != byte(session.channel.ID) || byte(state.RaidKey>>24) != byte(s.channelAdvertiseID()) {
		s.raidMu.Unlock()
		return 0, "", false, noop
	}
	battleID := ""
	if state.Started {
		b := state.Battle
		if b == nil || b.InstanceID == "" || b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
			s.raidMu.Unlock()
			return 0, "", false, noop
		}
		battleID = b.InstanceID
	}
	return state.RaidKey, battleID, true, s.raidMu.Unlock
}

// Leaving or losing admission uses the existing PVF raid Seria landing and
// exact op36 route. Coordinates are resolved by that existing owner, not guessed.
func (s *Service) returnCurrentAntonMemberToGate(session *gameSession) error {
	if session == nil || (session.channel.Type != 23 && session.channel.Type != 32) || !s.currentRaidAuthenticated(session) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	_, townID, areaID, row, _, err := s.currentRaidEntranceTownTransition(session, session.selectedCharacterID, c)
	if err != nil {
		return err
	}
	var writer packetWriter
	writer.writeByte(townID)
	writer.writeByte(areaID)
	writer.writeUint16(row.Value1)
	writer.writeUint16(row.Value2)
	writer.writeByte(row.Value3)
	writer.writeUint16(0)
	writer.writeUint16(0)
	writer.writeUint32(0)
	writer.writeByte(0)
	return s.handleTownSetUserAreaWithQuestNotice(session, writer.bytes(), false)
}

// Rebucket the already admitted waiting members together when their frozen
// battle starts. Their actor identity and coordinates stay unchanged.
func (m *OnlinePlayerManager) bindAntonWaitingBattleInstance(raidKey uint32, battleID string) {
	if m == nil || raidKey == 0 || battleID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]uint16, 0)
	for id, key := range m.byChar {
		if ((key.TownID == 19 && key.AreaID == 2) || (key.TownID == 32 && key.AreaID == 4)) && key.Instance == raidKey {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		key := m.byChar[id]
		if !((key.TownID == 19 && key.AreaID == 2) || (key.TownID == 32 && key.AreaID == 4)) || key.Instance != raidKey {
			continue
		}
		info := m.byArea[key][id]
		if info == nil {
			continue
		}
		m.removeFromAreaLocked(id)
		key.RaidBattleInstance = battleID
		info.RaidBattleInstance = battleID
		if m.byArea[key] == nil {
			m.byArea[key] = make(map[uint16]*onlinePlayerInfo)
		}
		m.byArea[key][id] = info
		m.byChar[id] = key
	}
}

func (s *Service) currentAntonWaitingAreaPresence(session *gameSession) bool {
	if s == nil || s.onlinePlayers == nil || session == nil {
		return false
	}
	p, found := s.onlinePlayers.PlayerForCharacter(session.selectedCharacterID)
	return found && p.Session == session && p.Instance != 0 && s.currentAntonWaitingArea(p.TownID, p.AreaID)
}
