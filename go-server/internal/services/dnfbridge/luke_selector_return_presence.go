package dnfbridge

// The completed-run selector no longer owns a dungeon runtime. Preserve the
// exact successful op24 destination until a town-side request confirms it.
// A raid membership or connection change must not reuse that old return.
type currentSelectorReturnPresence struct {
	identity   boundGameSessionCharacter
	channelID  int
	raidKey    uint32
	battleID   string
	transition currentDungeonTownTransition
}

// Followers are provisionally installed before the leader's admission can
// still fail. Retire the old selector owner only after the whole group commits;
// an admission rollback must leave its pending town return available.
func clearCurrentSelectorReturnAfterCommittedPartyEntry(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil || runtime == nil {
		return
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime == runtime && session.selectorReturnPresence != nil {
		session.selectorReturnPresence = nil
		session.confirmedDungeonReturnStatePending = false
		session.returnTownFinishLoadingAckOnly = false
	}
}

// Caller holds dungeon.mu. Only Luke waiting-room selector returns need this
// repair; ordinary towns and active-runtime returns keep their existing owner.
func (s *Service) captureCurrentSelectorReturnPresence(session *gameSession, transition currentDungeonTownTransition) *currentSelectorReturnPresence {
	if !s.currentLukeWaitingArea(transition.TownID, transition.AreaID) {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil
	}
	key, battle, admitted, release := s.lockCurrentAntonWaitingAreaScope(session, transition.TownID, transition.AreaID)
	defer release()
	if !admitted || key == 0 || battle == "" {
		return nil
	}
	return &currentSelectorReturnPresence{identity: identity, channelID: session.channel.ID, raidKey: key, battleID: battle, transition: cloneCurrentDungeonTownTransition(transition)}
}

func (s *Service) currentSelectorReturnPresenceOwner(session *gameSession, owner *currentSelectorReturnPresence) bool {
	if s == nil || session == nil || owner == nil || owner.identity.session != session ||
		session.channel.ID != owner.channelID || session.channel.Type != 32 ||
		!session.loginPacketSeen || !session.accountFromToken || !s.boundGameSessionCharacterCurrent(owner.identity) {
		return false
	}
	// This immutable owner was authenticated when op24 succeeded. Distinguish
	// an actually replaced binding/battle from a transient repository failure:
	// final publication still performs the existing repository admission check.
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(owner.identity.character)
	if state == nil || !state.CurrentLayout || !state.Started || state.RouteOrRaidType != 1 ||
		state.RaidKey != owner.raidKey || byte(state.RaidKey>>16) != byte(session.channel.ID) ||
		byte(state.RaidKey>>24) != byte(s.channelAdvertiseID()) {
		return false
	}
	battle := state.Battle
	return battle != nil && battle.InstanceID == owner.battleID && battle.ChannelID == session.channel.ID &&
		battle.ChannelType == session.channel.Type && battle.MemberAccounts[owner.identity.character] == s.accountIDForSession(session)
}

func (s *Service) restoreCurrentSelectorReturnPresence(session *gameSession, owner *currentSelectorReturnPresence, source string) bool {
	if !s.currentSelectorReturnPresenceOwner(session, owner) || s.sessionHasActiveDungeonRuntime(session) {
		return false
	}
	tr := owner.transition
	session.townMu.Lock()
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: owner.identity.character, TownID: tr.TownID, AreaID: tr.AreaID, PositionX: uint16(tr.PositionX), PositionY: uint16(tr.PositionY), MovementCode: tr.Direction, PositionValid: true}
	session.townMu.Unlock()
	// Reuse the typed actor/appearance/roster broadcaster. Final registration
	// checks this same frozen raid scope while holding the membership lock.
	return s.rebindTownCopresenceAfterDungeonReturn(session, tr.TownID, tr.AreaID, source+"_confirmed_luke_selector_return", owner)
}
