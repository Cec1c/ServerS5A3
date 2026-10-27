// game_sessions.go 维护 DNF game 连接到在线角色的短生命周期索引。
// 这里的状态只用于旧客户端实时回包投影，不是队伍、角色或玩法的持久 owner。
package dnfbridge

import (
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

type boundGameSessionCharacter struct {
	session    *gameSession
	character  uint16
	generation uint64
}

func (s *Service) bindGameSessionCharacter(session *gameSession, charID uint16) {
	if session != nil {
		session.darkKnightCombo.reset()
		session.initialTownGuardianShieldDeckSent = false
		session.initialTownGuardianShieldAppearanceSent = false
	}
	if s == nil || session == nil || charID == 0 {
		return
	}
	guildClock := s.ensureGameplayTimeQueue()
	var guildObservations []currentGuildPresenceObservation
	defer func() { s.persistCurrentGuildPresence(guildObservations...) }()
	if oldID := session.selectedCharacterID; oldID != 0 && oldID != charID {
		s.closeCurrentGuildCreateSession(session)
		s.closeCurrentPVPSession(session)
		s.closeCurrentExpertJobStoreSession(session, true)
		s.detachOnlineItemTrade(session, "selected_character_changed")
		s.detachRuntimePartySession(session, "selected_character_changed")
		s.stopCurrentSpendTimeClock(session, "selected_character_changed")
		s.stopCurrentPetGrowthClock(session, "selected_character_changed")
		s.stopCurrentDailyDeleteClock(session)
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if oldID := session.selectedCharacterID; oldID != 0 && oldID != charID {
		s.cancelCurrentDungeonDeathReturnLocked(session, session.dungeon.runtime, "selected_character_changed")
		s.cancelCurrentCrystalContractTimerLocked(session, session.dungeon.runtime, "selected_character_changed")
		s.cancelCurrentDungeonCardAutoFlipLocked(session, session.dungeon.runtime, "selected_character_changed")
		s.cancelCurrentTournamentRewardAutoSelectTimerLocked(session, session.dungeon.runtime, "selected_character_changed")
	}
	resident := (*gameSession)(nil)
	residentParty := alignedcmd.PartyState{}
	if s.onlinePlayers != nil {
		resident = s.onlinePlayers.SessionForCharacter(charID)
		if resident != nil && resident != session && session.channelReconnect {
			// A channel-reconnect session binds before its op13 inventory
			// bootstrap. Settle and stop the resident clock synchronously here,
			// so any threshold reward is already durable and therefore included
			// in that new bootstrap. Waiting for the old socket's later unbind
			// would allow the reward commit to race after op13 and remain visually
			// absent until another login.
			s.stopCurrentSpendTimeClock(resident, "replacement_game_session_bound_before_inventory_bootstrap")
			s.stopCurrentDailyDeleteClock(resident)
			residentParty = runtimePartyStateSnapshot(resident)
		} else if resident != nil && resident != session {
			residentParty = runtimePartyStateSnapshot(resident)
		}
	}
	s.lockCurrentCharacterBindingMutation(session, charID)
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	if oldID := session.selectedCharacterID; oldID != 0 && oldID != charID {
		if s.gameSessions[oldID] == session {
			guildObservations = append(guildObservations, s.currentGuildPresenceObservationLocked(session, guildClock.Now()))
			delete(s.gameSessions, oldID)
		}
	}
	previous := s.gameSessions[charID]
	characterChanged := session.selectedCharacterID != charID
	if characterChanged || previous != session {
		s.clearCurrentPeerProfileReferencesLocked(session)
	}
	if characterChanged || session.characterGeneration == 0 ||
		(previous != nil && previous != session && session.characterGeneration == previous.characterGeneration) {
		s.allocateGameSessionCharacterGenerationLocked(session, previous)
	} else if session.characterGeneration > s.nextGameSessionGeneration {
		s.nextGameSessionGeneration = session.characterGeneration
	}
	if characterChanged {
		session.expertJobInfoCharacterID = 0
	}
	session.selectedCharacterID = charID
	if resident != nil && resident != session {
		if current := s.gameSessions[charID]; current == resident {
			storeRuntimePartyState(session, residentParty)
			s.mu.Unlock()
			return
		}
	}
	s.gameSessions[charID] = session
	if previous != nil && previous != session {
		s.clearCurrentPeerProfileReferencesLocked(previous)
	}
	if previous != session || characterChanged {
		guildObservations = append(guildObservations, s.currentGuildPresenceObservationLocked(session, guildClock.Now()))
	}
	s.mu.Unlock()
	if previous != nil && previous != session {
		s.closeCurrentGuildCreateSession(previous)
		s.closeCurrentPVPSession(previous)
		s.rebindManagedRuntimePartySession(previous, session)
	}
	s.restoreCurrentRaidPresence(session)
}

// allocateGameSessionCharacterGenerationLocked assigns a nonce that is unique
// across the bridge process. RuntimePartyManager stores this generation without
// a session pointer, so a reconnect must never reuse the retired socket's
// initial value (both fresh sockets otherwise begin at generation one).
// s.mu must be held.
func (s *Service) allocateGameSessionCharacterGenerationLocked(session *gameSession, competing *gameSession) uint64 {
	if s == nil || session == nil {
		return 0
	}
	next := s.nextGameSessionGeneration
	if session.characterGeneration > next {
		next = session.characterGeneration
	}
	if competing != nil && competing.characterGeneration > next {
		next = competing.characterGeneration
	}
	next++
	if next == 0 {
		next++
	}
	s.nextGameSessionGeneration = next
	session.characterGeneration = next
	return next
}

func (s *Service) promoteResidentGameSession(session *gameSession, charID uint16) {
	if s == nil || session == nil || charID == 0 || session.selectedCharacterID != charID {
		return
	}
	s.lockCurrentCharacterBindingMutation(session, charID)
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	previous := s.gameSessions[charID]
	s.gameSessions[charID] = session
	s.mu.Unlock()
	if previous != nil && previous != session {
		s.closeCurrentGuildCreateSession(previous)
		s.closeCurrentPVPSession(previous)
		s.rebindManagedRuntimePartySession(previous, session)
	}
}

// rebindManagedRuntimePartySession promotes a channel-reconnect socket without
// treating it as a leave/join. The old socket's later close is generation
// stale and therefore cannot remove the replacement from the party.
func (s *Service) rebindManagedRuntimePartySession(previous *gameSession, replacement *gameSession) {
	if previous == nil || replacement == nil || previous.selectedCharacterID == 0 || previous.selectedCharacterID != replacement.selectedCharacterID {
		return
	}
	priorGeneration := previous.characterGeneration
	identity, current := s.boundGameSessionCharacterSnapshot(replacement)
	if !current || priorGeneration == 0 {
		return
	}
	member, memberOK := s.runtimePartyMemberForIdentity(identity)
	if !memberOK {
		return
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return
	}
	result := manager.RebindSession(identity.character, priorGeneration, member)
	if result.OK {
		s.publishRuntimePartySnapshot(result.Party)
		return
	}
	// The first reconnect after deploying the central manager can still carry
	// only the legacy client projection. Import that exact projection under the
	// new session generation rather than leaving the replacement partyless.
	s.bootstrapManagedRuntimePartyForSession(replacement)
}

func (s *Service) unbindGameSession(session *gameSession) {
	s.closeCurrentPVPSession(session)
	if s == nil || session == nil {
		return
	}
	s.retireCurrentTagTournamentBattle(session)
	guildClock := s.ensureGameplayTimeQueue()
	s.closeCurrentGuildCreateSession(session)
	s.unregisterLiveGameSession(session)
	s.mu.Lock()
	canonical := session.selectedCharacterID != 0 && s.gameSessions[session.selectedCharacterID] == session
	s.mu.Unlock()
	if canonical {
		s.detachOnlineItemTrade(session, "game_session_unbound_or_disconnected")
		// 角色目录里那条会话关闭 ≠ 角色下线：当前客户端会给同一个角色同时开两条活着的
		// 传输（现场 2026-09-26：char 4 有一条 ~8 条事件的短命辅助连接 + 一条 400+ 条
		// 事件的主连接）。目录 owner 恰好是那条辅助连接时，它的关闭会把**仍在另一条
		// socket 上玩的角色**从队伍里摘掉：现场症状正是"队伍明明成立，队长选图时
		// party_id=0 / member_count=0，且没有任何 leave 事件"（摘除发生在 detach 里，
		// 只以 op9 移除帧的形式落日志）。所以这里先把目录与队伍**交接**给另一条活着的
		// 同角色会话；只有确认这个角色已无任何活会话时才真的 detach。
		if !s.handoverRuntimePartyToLiveSession(session, "game_session_unbound_or_disconnected") {
			s.detachRuntimePartySession(session, "game_session_unbound_or_disconnected")
		}
	}
	s.cancelCurrentDungeonDeathReturn(session, "game_session_unbound_or_disconnected")
	session.dungeon.mu.Lock()
	s.cancelCurrentDungeonCardAutoFlipLocked(session, session.dungeon.runtime, "game_session_unbound_or_disconnected")
	s.cancelCurrentCrystalContractTimerLocked(session, session.dungeon.runtime, "game_session_unbound_or_disconnected")
	s.cancelCurrentTournamentRewardAutoSelectTimerLocked(session, session.dungeon.runtime, "game_session_unbound_or_disconnected")
	session.dungeon.mu.Unlock()
	s.stopCurrentPetGrowthClock(session, "game_session_unbound_or_disconnected")
	s.stopCurrentDailyDeleteClock(session)
	s.stopCurrentSpendTimeClock(session, "game_session_unbound_or_disconnected")
	s.stopAvatarAppearanceCoalesce(session, "game_session_unbound_or_disconnected")
	s.lockCurrentCharacterBindingMutation(session, 0)
	s.clearCurrentPeerProfileReferencesLocked(session)
	presenceChanged := false
	guildObservation := s.currentGuildPresenceObservationLocked(session, guildClock.Now())
	if session.selectedCharacterID != 0 && s.gameSessions[session.selectedCharacterID] == session {
		delete(s.gameSessions, session.selectedCharacterID)
		presenceChanged = true
	}
	if session.selectedCharacterID != 0 {
		advanceGameSessionCharacterGeneration(session)
	}
	s.mu.Unlock()
	if presenceChanged {
		s.refreshCurrentAccountFriendPresence(session, "offline")
		s.persistCurrentGuildPresence(guildObservation)
	}
}

// registerLiveGameSession / unregisterLiveGameSession 维护"活着的 game 传输"索引。
// 它只回答一个问题：同一个角色此刻还有没有别的活 socket。见 Service.liveGameSessions。
func (s *Service) registerLiveGameSession(session *gameSession) {
	if s == nil || session == nil || session.connID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveGameSessions == nil {
		s.liveGameSessions = make(map[string]*gameSession)
	}
	s.liveGameSessions[session.connID] = session
}

func (s *Service) unregisterLiveGameSession(session *gameSession) {
	if s == nil || session == nil || session.connID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveGameSessions[session.connID] == session {
		delete(s.liveGameSessions, session.connID)
	}
}

// liveSameCharacterSessions 返回**除 exclude 以外**仍然活着、并且已经绑定到同一个角色的
// 会话（按 conn_id 升序，确定性）。只用于交接与诊断。
func (s *Service) liveSameCharacterSessions(characterID uint16, exclude *gameSession) []*gameSession {
	if s == nil || characterID == 0 {
		return nil
	}
	s.mu.Lock()
	others := make([]*gameSession, 0, 2)
	for _, session := range s.liveGameSessions {
		if session == nil || session == exclude || session.conn == nil {
			continue
		}
		if session.selectedCharacterID != characterID {
			continue
		}
		others = append(others, session)
	}
	s.mu.Unlock()
	sort.Slice(others, func(i, j int) bool { return others[i].connID < others[j].connID })
	return others
}

// liveSameCharacterConnIDs 只给日志用（`game-000016,game-000013` 这种形状）。
func (s *Service) liveSameCharacterConnIDs(characterID uint16, exclude *gameSession) string {
	others := s.liveSameCharacterSessions(characterID, exclude)
	parts := make([]string, 0, len(others))
	for _, session := range others {
		parts = append(parts, session.connID)
	}
	return strings.Join(parts, ",")
}

// handoverRuntimePartyToLiveSession 在"角色目录 owner 那条会话即将关闭"时，把目录与运行期
// 队伍交接给这个角色**另一条活着的会话**，并返回是否发生了交接。
//
// 为什么必须交接（现场 2026-09-26）：客户端给同一个角色同时开两条活传输，哪一条成为目录
// owner 取决于绑定顺序（`bindGameSessionCharacter` 的早退分支刻意不让第二条连接抢目录）。
// 如果队伍是在"辅助连接"那条上成立的，辅助连接一关，`detachRuntimePartySession` 就把
// **仍在主连接上玩的角色**从队伍里摘掉——管理器和会话投影同时清空，客户端那边只看到一条
// op9 移除帧，于是队长随后选图时 `party_id=0`，进图静默退化成单人。
//
// 交接内容：① 目录改指替代会话（它之后的队伍读取/发布才走对会话）；② 把管理器里该角色的
// 成员记录改绑到替代会话的代际（按**角色**查，不看代际——记录的代际是"入队时那条 socket"的）；
// ③ 把权威名单重画给替代会话。任何一步不成立都只留日志、不抛错，最坏情况退化成改动前的
// detach 语义（由调用方决定）。
func (s *Service) handoverRuntimePartyToLiveSession(closing *gameSession, source string) bool {
	if s == nil || closing == nil || closing.selectedCharacterID == 0 {
		return false
	}
	characterID := closing.selectedCharacterID
	others := s.liveSameCharacterSessions(characterID, closing)
	if len(others) == 0 {
		return false
	}
	replacement := s.pickHandoverReplacement(characterID, others)
	if replacement == nil || replacement.selectedCharacterID != characterID ||
		replacement.characterGeneration == 0 {
		return false
	}
	// 先把目录交给替代会话，**再**去解析成员记录：替代会话此刻还不是目录 owner，
	// `boundGameSessionCharacterSnapshot` / `runtimePartyMemberForIdentity` 都要求
	// "会话就是角色目录里那一条"，顺序反了就会永远交不出去。
	s.lockCurrentCharacterBindingMutation(closing, characterID)
	if s.gameSessions[characterID] != closing {
		// 目录已经不在关闭会话手上：不抢。
		s.mu.Unlock()
		return false
	}
	s.gameSessions[characterID] = replacement
	s.mu.Unlock()
	identity, current := s.boundGameSessionCharacterSnapshot(replacement)
	if !current || identity.character != characterID {
		return false
	}

	reboundPartyID := 0
	reboundMembers := 0
	rebindResult := "no_managed_party"
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if snapshot, found := manager.SnapshotByUser(characterID, 0); found {
			recordedGeneration := uint64(0)
			for _, member := range snapshot.Members {
				if member.UserID == characterID {
					recordedGeneration = member.SessionGeneration
					break
				}
			}
			member, memberOK := s.runtimePartyMemberForIdentity(identity)
			switch {
			case !memberOK:
				rebindResult = "replacement_session_not_bound"
			case recordedGeneration == 0:
				rebindResult = "party_records_no_generation_for_character"
			default:
				if result := manager.RebindSession(characterID, recordedGeneration, member); result.OK {
					state := s.publishRuntimePartySnapshot(result.Party)
					reboundPartyID = state.PartyID
					reboundMembers = len(runtimePartyMembers(state))
					rebindResult = "rebound_to_replacement_session"
					// 替代会话此前可能一条名册都没收到（名册都发给了关闭的那条）。
					// 只补 op153 + op9，**绝不重放 op11**（刚建好的 UDP 对端再握手会把
					// 客户端打回 connecting，见 party_online.go 的既有结论）。
					if err := s.sendRuntimePartyRosterLocal(
						replacement,
						state,
						"session_handover_roster_rebind",
					); err != nil {
						s.logWarn("dnfbridge party session handover roster rebind failed",
							"conn_id", replacement.connID,
							"char_id", characterID,
							"error", err)
					}
				} else {
					rebindResult = "rebind_failed:" + result.Reason
				}
			}
		}
	}
	s.logGameEvent(replacement, "game-party-session-handover",
		"conn_id", replacement.connID,
		"closing_conn_id", closing.connID,
		"char_id", characterID,
		"closing_generation", closing.characterGeneration,
		"replacement_generation", identity.generation,
		"source", source,
		"rebind", rebindResult,
		"party_id", reboundPartyID,
		"member_count", reboundMembers,
		"live_same_character_conns", s.liveSameCharacterConnIDs(characterID, replacement),
		"reason", "character_directory_owner_socket_closed_while_the_character_is_still_online_elsewhere")
	return true
}

// pickHandoverReplacement 在同一个角色的多条活会话里挑交接对象：优先"城镇在线表里登记着的
// 那条"（客户端真正在用的那条），否则取 conn_id 最大的（最新建立的传输）。
func (s *Service) pickHandoverReplacement(characterID uint16, others []*gameSession) *gameSession {
	if len(others) == 0 {
		return nil
	}
	if s.onlinePlayers != nil {
		if resident := s.onlinePlayers.SessionForCharacter(characterID); resident != nil {
			for _, session := range others {
				if session == resident {
					return session
				}
			}
		}
	}
	return others[len(others)-1]
}

func (s *Service) onlineGameSession(charID uint16) (*gameSession, bool) {
	if s == nil || charID == 0 {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.gameSessions[charID]
	return session, ok && session != nil
}

func (s *Service) boundGameSessionCharacterSnapshot(session *gameSession) (boundGameSessionCharacter, bool) {
	if s == nil || session == nil {
		return boundGameSessionCharacter{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	characterID := session.selectedCharacterID
	if characterID == 0 || s.gameSessions[characterID] != session {
		return boundGameSessionCharacter{}, false
	}
	return boundGameSessionCharacter{
		session:    session,
		character:  characterID,
		generation: session.characterGeneration,
	}, true
}

func (s *Service) onlineGameSessionCharacterSnapshot(characterID uint16) (boundGameSessionCharacter, bool) {
	if s == nil || characterID == 0 {
		return boundGameSessionCharacter{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.gameSessions[characterID]
	if session == nil || session.selectedCharacterID != characterID {
		return boundGameSessionCharacter{}, false
	}
	return boundGameSessionCharacter{
		session:    session,
		character:  characterID,
		generation: session.characterGeneration,
	}, true
}

func (s *Service) boundGameSessionCharacterCurrent(identity boundGameSessionCharacter) bool {
	if s == nil || identity.session == nil || identity.character == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gameSessions[identity.character] == identity.session &&
		identity.session.selectedCharacterID == identity.character &&
		identity.session.characterGeneration == identity.generation
}
