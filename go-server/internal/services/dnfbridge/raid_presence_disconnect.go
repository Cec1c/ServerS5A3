package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	"strconv"
	"strings"
	"time"
)

// Seat state is transient like the raid aggregate. Unique socket/generation and
// aggregate pointer prevent an old shutdown/sweep from retiring a later owner.
type currentRaidDisconnectSeat struct {
	Raid           *runtimeRaidState
	RaidKey        uint32
	CharacterID    uint16
	Session        *gameSession
	Generation     uint64
	AccountID      string
	InstanceID     string
	DisconnectedAt time.Time
	ExpiresAt      time.Time
}

func (s *Service) raidPresenceContext(session *gameSession, state runtimeRaidState, account string) bool {
	if session == nil || !state.CurrentLayout || session.channel.ID < 0 || session.channel.ID > 255 {
		return false
	}
	route, ok := currentRaidRouteForChannel(int(session.channel.Type))
	server := s.channelAdvertiseID()
	if !ok || route != state.RouteOrRaidType || server < 0 || server > 255 || byte(state.RaidKey>>24) != byte(server) || byte(state.RaidKey>>16) != byte(session.channel.ID) {
		return false
	}
	if b := state.Battle; b != nil {
		return b.ChannelID == session.channel.ID && b.ChannelType == session.channel.Type && b.MemberAccounts[session.selectedCharacterID] == account
	}
	return true
}
func (s *Service) noteCurrentRaidDisconnect(identity boundGameSessionCharacter) {
	session := identity.session
	if s == nil || s.options.currentClientVersion != "official-24.9" || session == nil || identity.character == 0 || identity.generation == 0 || !session.loginPacketSeen || !session.accountFromToken {
		return
	}
	account := strings.TrimSpace(session.accountID)
	if account == "" {
		return
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	owner, found, err := g.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	cancel()
	if err != nil || !found || owner.CharacterID != strconv.Itoa(int(identity.character)) || owner.AccountID != account {
		return
	}
	snapshot, found := s.runtimeRaidForMember(identity.character)
	if !found || !s.raidPresenceContext(session, snapshot, account) {
		return
	}
	var seconds uint32
	if snapshot.Battle != nil {
		seconds = snapshot.Battle.Configuration.DisconnectWaitSeconds
	}
	if seconds == 0 && snapshot.RouteOrRaidType == 2 {
		if c, err := s.loadCurrentFiendWarConfig(); err == nil {
			seconds = uint32(c.DisconnectMemberWaitSeconds)
		}
	}
	if seconds == 0 {
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return
		}
		path := "etc/raid/anton.etc"
		if snapshot.RouteOrRaidType == 1 {
			path = "etc/raid/luke/luke.etc"
		}
		config, err := raid.LoadBattleConfiguration(archive, path)
		if err != nil {
			return
		}
		seconds = config.DisconnectWaitSeconds
	}
	if seconds == 0 {
		return
	}
	now := s.gameplayNow()
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(identity.character)
	if state == nil || state.RaidKey != snapshot.RaidKey || !s.raidPresenceContext(session, *state, account) {
		s.raidMu.Unlock()
		return
	}
	s.mu.Lock()
	canonical := s.gameSessions[identity.character]
	// unbind may have handed the directory to the other live same-character
	// transport. It still owns this raid; an auxiliary close is not a disconnect.
	online := canonical != nil && canonical != session && canonical.selectedCharacterID == identity.character && canonical.loginPacketSeen && canonical.accountFromToken && strings.TrimSpace(canonical.accountID) == account && s.raidPresenceContext(canonical, *state, account)
	if online {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return
	}
	if old, exists := s.currentRaidDisconnects[identity.character]; exists && (old.Session == session || old.Generation >= identity.generation) {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return
	}
	idx := runtimeRaidMemberIndex(*state, identity.character)
	if idx < 0 {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return
	}
	if s.currentRaidDisconnects == nil {
		s.currentRaidDisconnects = map[uint16]currentRaidDisconnectSeat{}
	}
	instance := ""
	if state.Battle != nil {
		instance = state.Battle.InstanceID
	}
	s.currentRaidDisconnects[identity.character] = currentRaidDisconnectSeat{Raid: state, RaidKey: state.RaidKey, CharacterID: identity.character, Session: session, Generation: identity.generation, AccountID: account, InstanceID: instance, DisconnectedAt: now, ExpiresAt: now.Add(time.Duration(seconds) * time.Second)}
	state.Members[idx].UserState = 0
	s.mu.Unlock()
	copy := cloneRuntimeRaidState(*state)
	s.raidMu.Unlock()
	s.publishCurrentRaidPresence(copy, identity.character)
}
func (s *Service) restoreCurrentRaidPresence(session *gameSession) {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	s.raidMu.Lock()
	_, reserved := s.currentRaidDisconnects[session.selectedCharacterID]
	s.raidMu.Unlock()
	if !reserved || !s.currentRaidAuthenticated(session) {
		return
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return
	}
	account := strings.TrimSpace(session.accountID)
	s.raidMu.Lock()
	seat, found := s.currentRaidDisconnects[identity.character]
	state := s.runtimeRaidByMemberLocked(identity.character)
	if !found || state == nil || state != seat.Raid || state.RaidKey != seat.RaidKey || seat.AccountID != account || !s.raidPresenceContext(session, *state, account) {
		s.raidMu.Unlock()
		return
	}
	if state.Battle != nil && seat.InstanceID != "" && seat.InstanceID != state.Battle.InstanceID {
		s.raidMu.Unlock()
		return
	}
	// Restore must precede the resource deadline, even if the one-second
	// sweep was delayed. A socket bound after expiry cannot revive the seat.
	now := s.gameplayNow()
	if now.Before(seat.DisconnectedAt) {
		s.raidMu.Unlock()
		return
	}
	if !now.Before(seat.ExpiresAt) {
		s.raidMu.Unlock()
		s.expireCurrentRaidDisconnectSeats(now)
		return
	}
	s.mu.Lock()
	if s.gameSessions[identity.character] != session || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return
	}
	delete(s.currentRaidDisconnects, identity.character)
	if b := state.Battle; b != nil && b.FiendWar != nil {
		next := b.FiendWar.Clone()
		next.MemberAreas[identity.character] = next.Config.WaitingArea
		b.FiendWar = next
		b.ProgressVersion++
	}
	state.Members[runtimeRaidMemberIndex(*state, identity.character)].UserState = 1
	s.mu.Unlock()
	copy := cloneRuntimeRaidState(*state)
	s.raidMu.Unlock()
	// A fresh bound client's Raid object is not installed yet. Its normal owned
	// info/rejoin path restores self; only already-installed peers are refreshed.
	s.publishCurrentRaidPresence(copy, identity.character)
}
func (s *Service) expireCurrentRaidDisconnectSeats(now time.Time) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || now.IsZero() {
		return
	}
	changed := map[uint32]runtimeRaidState{}
	expiredParticipation := map[uint32]bool{}
	s.raidMu.Lock()
	for id, seat := range s.currentRaidDisconnects {
		state := s.raids[seat.RaidKey]
		if state == nil || state != seat.Raid || runtimeRaidMemberIndex(*state, id) < 0 {
			delete(s.currentRaidDisconnects, id)
			continue
		}
		if state.Battle != nil && seat.InstanceID != "" && state.Battle.InstanceID != seat.InstanceID {
			delete(s.currentRaidDisconnects, id)
			continue
		}
		s.mu.Lock()
		current := s.gameSessions[id]
		online := current != nil && current != seat.Session && current.selectedCharacterID == id && current.loginPacketSeen && current.accountFromToken && strings.TrimSpace(current.accountID) == seat.AccountID && s.raidPresenceContext(current, *state, seat.AccountID)
		if online && !now.Before(seat.DisconnectedAt) && now.Before(seat.ExpiresAt) {
			delete(s.currentRaidDisconnects, id)
			state.Members[runtimeRaidMemberIndex(*state, id)].UserState = 1
			s.mu.Unlock()
			changed[state.RaidKey] = cloneRuntimeRaidState(*state)
			continue
		}
		if now.Before(seat.ExpiresAt) {
			s.mu.Unlock()
			continue
		}
		delete(s.currentRaidDisconnects, id)
		// The immutable Battle/reward journal still owns accepted clears/assets.
		// Only the retained directory seat expires.
		departedLeader := state.LeaderID == id
		hadParticipation := false
		if state.Battle != nil {
			_, hadParticipation = state.Battle.Participation[id]
		}
		_, removed, disbanded := s.removeRuntimeRaidMemberLocked(id)
		if disbanded && state.Battle != nil {
			// Accepted battle and uncommitted reward/weekly intents outlive
			// the last directory seat. Rejoin cannot find an empty roster,
			// while the existing clock can still finish its frozen journal.
			state.LeaderID = 0
			s.raids[seat.RaidKey] = state
		}
		s.mu.Unlock()
		if removed && !disbanded {
			if departedLeader && state.LeaderID != 0 {
				leaderIdx := runtimeRaidMemberIndex(*state, state.LeaderID)
				if leaderIdx >= 0 && state.Members[leaderIdx].UserState == 0 {
					members := append([]runtimeRaidMemberState(nil), state.Members...)
					runtimeRaidSortMembers(members)
					for _, m := range members {
						if m.UserState != 0 {
							state.LeaderID = m.CharID
							break
						}
					}
				}
			}
			changed[state.RaidKey] = cloneRuntimeRaidState(*state)
			if hadParticipation {
				expiredParticipation[state.RaidKey] = true
			}
		}
	}
	// One final roster per aggregate: expiring two seats in the same sweep
	// must not send an intermediate roster that still contains an expired peer.
	for key := range changed {
		if latest := s.raids[key]; latest != nil {
			changed[key] = cloneRuntimeRaidState(*latest)
		} else {
			delete(changed, key)
		}
	}
	s.raidMu.Unlock()
	for _, state := range changed {
		s.publishCurrentRaidPresence(state, 0)
		if expiredParticipation[state.RaidKey] {
			s.queueCurrentRaidDungeonProgress(state)
		}
	}
}
func (s *Service) publishCurrentRaidPresence(state runtimeRaidState, excluded uint16) {
	body, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		s.logWarn("raid presence body failed", "error", err)
		return
	}
	for _, member := range state.Members {
		if member.CharID == excluded {
			continue
		}
		peer, ok := s.onlineGameSession(member.CharID)
		if !ok || !s.currentRaidInfoVisible(peer, state) || !s.raidPresenceContext(peer, state, strings.TrimSpace(peer.accountID)) {
			continue
		}
		if err := s.sendGameUpperRawClassCodec(peer, raid.RaidMemberRefreshMsgID, body, 0, true); err != nil {
			s.logWarn("raid presence projection failed", "error", fmt.Sprint(err))
		}
	}
}
