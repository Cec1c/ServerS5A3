package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The formal 369d town19/area2 map and gate27, also observed in current
// client op36, identify the Anton entrance. Main deck and Seria stay separate.
// Retain this existing hook name for town/raid callers; the exact Luke program
// waiting room shares the same admission, presence and removal lifecycle.
func (s *Service) currentAntonWaitingArea(townID, areaID byte) bool {
	if s.currentLukeWaitingArea(townID, areaID) || s.currentFiendWarTownArea(townID, areaID) {
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
	if s.currentFiendWarTownArea(townID, areaID) {
		channelType, route = 42, 2
	}
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
	if session == nil || (session.channel.Type != 23 && session.channel.Type != 32 && session.channel.Type != 42) || !s.currentRaidAuthenticated(session) {
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

type currentRaidWaitingBattleMember struct {
	accountID string
	session   *gameSession
}

type currentRaidWaitingBattleScope struct {
	raidKey   uint32
	battleID  string
	channelID int
	townID    byte
	areaID    byte
	members   map[uint16]currentRaidWaitingBattleMember
}

// 调用方持有raidMu：在扣入场材料前冻结真实PVF等待区及本场成员，
// 不能只凭在线表中可能残留的团号认定谁属于本次开团。
func (s *Service) prepareCurrentRaidWaitingBattleScope(session *gameSession, state runtimeRaidState, battle *runtimeRaidBattle) (currentRaidWaitingBattleScope, error) {
	if battle == nil {
		return currentRaidWaitingBattleScope{}, nil
	}
	if s == nil || session == nil || state.RaidKey == 0 || state.LeaderID != session.selectedCharacterID || battle.InstanceID == "" || battle.ChannelID != session.channel.ID || battle.ChannelType != session.channel.Type {
		return currentRaidWaitingBattleScope{}, fmt.Errorf("团本等待区战斗归属不符")
	}
	route, supported := currentRaidRouteForChannel(int(session.channel.Type))
	if !supported || state.RouteOrRaidType != route || battle.Mode != route+1 {
		return currentRaidWaitingBattleScope{}, fmt.Errorf("团本等待区模式不符")
	}
	rule, err := s.currentRaidAdmissionRule(session)
	if err != nil {
		return currentRaidWaitingBattleScope{}, err
	}
	if rule.WaitingTown == 0 || len(state.Members) == 0 {
		return currentRaidWaitingBattleScope{}, fmt.Errorf("团本等待区或成员未配置")
	}
	scope := currentRaidWaitingBattleScope{raidKey: state.RaidKey, battleID: battle.InstanceID, channelID: battle.ChannelID, townID: rule.WaitingTown, areaID: rule.WaitingArea, members: make(map[uint16]currentRaidWaitingBattleMember, len(state.Members))}
	for _, member := range state.Members {
		owner, found := s.onlineGameSessionCharacterSnapshot(member.CharID)
		account := battle.MemberAccounts[member.CharID]
		if !found || account == "" || !owner.session.loginPacketSeen || !owner.session.accountFromToken || owner.session.channel.ID != scope.channelID || owner.session.channel.Type != battle.ChannelType || s.accountIDForSession(owner.session) != account || !s.boundGameSessionCharacterCurrent(owner) || (member.CharID == state.LeaderID && owner.session != session) {
			return currentRaidWaitingBattleScope{}, fmt.Errorf("团本等待区成员会话归属不符")
		}
		scope.members[member.CharID] = currentRaidWaitingBattleMember{accountID: account, session: owner.session}
	}
	return scope, nil
}

// 安图恩、卢克和超时空共用。只重绑本场已冻结成员的当前等待区记录；
// 普通区域、其他频道/账号/连接及已属于另一场战斗的记录均保持原状。
func (m *OnlinePlayerManager) bindRaidWaitingBattleInstance(scope currentRaidWaitingBattleScope) {
	if m == nil || scope.raidKey == 0 || scope.battleID == "" || scope.channelID <= 0 || scope.townID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, member := range scope.members {
		key, found := m.byChar[id]
		if !found || id == 0 || member.accountID == "" || member.session == nil || key.Instance != scope.raidKey || key.ChannelID != scope.channelID || key.TownID != scope.townID || key.AreaID != scope.areaID || (key.RaidBattleInstance != "" && key.RaidBattleInstance != scope.battleID) {
			continue
		}
		info := m.byArea[key][id]
		if info == nil || info.CharacterID != id || info.AccountID != member.accountID || info.Session != member.session || info.TownID != key.TownID || info.AreaID != key.AreaID || info.ChannelID != key.ChannelID || info.Instance != key.Instance || info.RaidBattleInstance != key.RaidBattleInstance {
			continue
		}
		if key.RaidBattleInstance == scope.battleID {
			continue
		}
		m.removeFromAreaLocked(id)
		key.RaidBattleInstance = scope.battleID
		info.RaidBattleInstance = scope.battleID
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
