package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// An explicit personal giveup or leave-party request leaves the party after
// the town scene is confirmed, including after completion. Result-page team
// returns and automatic deadline returns keep their separate policies.
// The remaining members retain their dungeon runtime and shared instance.
func (s *Service) leaveRuntimePartyOnMemberGiveup(session *gameSession, instance *dungeonPartyInstance) error {
	_, err := s.leaveRuntimePartyDungeonMember(session, instance, "member_giveup_confirmed_town", true)
	return err
}

// A disconnect uses the same retained-instance membership transition as a
// confirmed personal return, but never writes to the socket being retired.
func (s *Service) leaveRuntimePartyDungeonMember(session *gameSession, instance *dungeonPartyInstance, source string, notifyDeparting bool) (bool, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !s.boundGameSessionCharacterCurrent(identity) || instance == nil {
		return false, nil
	}
	manager := s.runtimePartyManagerForService()
	before, found := manager.SnapshotByUser(identity.character, identity.generation)
	drifted := !found
	if drifted {
		// Only the current authoritative socket may repair recorded generation
		// drift. An old dungeon return must never remove a new party membership.
		if !s.boundGameSessionCharacterCurrent(identity) {
			return false, nil
		}
		before, found = manager.SnapshotByUser(identity.character, 0)
	}
	if !found || before.ID != instance.key.PartyID {
		return false, nil
	}
	peers := dungeonPartyInstanceMemberSessions(instance, identity.character)
	var next alignedcmd.PartyState
	var removed bool
	if s.options.currentClientVersion == "official-24.9" {
		// Preserve the active instance's identity, slots and surviving lobby.
		// A departing leader transfers ownership without retiring that instance.
		var recordedGeneration uint64
		for _, member := range before.Members {
			if member.UserID == identity.character {
				recordedGeneration = member.SessionGeneration
			}
		}
		var result party.RuntimePartyResult
		if !s.withCurrentDungeonPartyRetirement(identity, instance, func() bool {
			result = manager.LeaveDungeonExpected(identity.character, recordedGeneration, before.ID)
			return result.OK
		}) {
			return false, nil
		}
		removed = result.OK
		if removed {
			s.logRuntimePartyMembershipRemoved(source, identity.character, identity.generation, before, result)
			s.clearRuntimePartyProjection(identity.character, identity.generation)
			next = s.publishRuntimePartySnapshot(result.Party)
		}
	} else if drifted {
		next, _, removed = s.repairManagedRuntimePartyLeave(identity)
	} else {
		next, _, removed = s.leaveManagedRuntimeParty(identity, source)
	}
	if !removed {
		return false, nil
	}
	instance.mu.Lock()
	for _, member := range before.Members {
		if member.Slot < 8 {
			instance.retiredPartyRecordKeys[member.Slot] = member.UserID
		}
	}
	instance.mu.Unlock()
	s.partyDungeonLoadingBarrierForceRelease(before.ID, "member_left_party")
	var errs error
	if s.options.currentClientVersion != "official-24.9" {
		for _, peer := range peers {
			body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(identity.character), currentSceneObjectContext, s.currentSceneActorRoute())
			errs = errors.Join(errs, s.sendGameUpperRawClass(peer.Session, uint16(dnfenum.CmdPacketRecoverStamina), body, 0))
		}
	}
	if len(runtimePartyMembers(next)) < 2 {
		s.closeRuntimePartyUDPRelay(int(before.ID))
	} else {
		s.syncRuntimePartyUDPRelay(next)
	}
	if notifyDeparting {
		errs = errors.Join(errs, s.sendRuntimePartyDeparture(session, before.StateFor(before.Leader)))
	}
	if s.options.currentClientVersion == "official-24.9" {
		errs = errors.Join(errs, s.sendMemberGiveupSurvivorRosterAfterDepartureMode(next, instance, identity.character, !notifyDeparting, nil))
	} else if !notifyDeparting {
		s.queueDetachedPartyMemberRemoved(before.StateFor(before.Leader), next, identity.character)
	} else {
		errs = errors.Join(errs, s.broadcastRuntimePartyMemberRemoved(before.StateFor(before.Leader), next, identity.character, session))
	}
	s.logGameEvent(session, "game-party-member-giveup-left", "char_id", identity.character,
		"party_id", before.ID, "leader_char_id", before.Leader, "remaining_members", len(next.Members), "source", source)
	return true, errs
}

// A survivor can still be bound to a pre-entry selected-channel party record.
// Update that record and owner-0 dungeon records with the same retained roster.
func (s *Service) sendMemberGiveupSurvivorRoster(state alignedcmd.PartyState, instance *dungeonPartyInstance) error {
	return s.sendMemberGiveupSurvivorRosterAfterDeparture(state, instance, 0)
}

func (s *Service) sendMemberGiveupSurvivorRosterAfterDeparture(state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16) error {
	return s.sendMemberGiveupSurvivorRosterAfterDepartureMode(state, instance, departingID, false, nil)
}

func (s *Service) sendMemberGiveupSurvivorRosterAfterDepartureMode(state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16, deferPeers bool, only *gameSession) error {
	var errs error
	members := runtimePartyMembers(state)
	// The return caller holds its own dungeon lock. Never acquire a peer's
	// session lock: simultaneous returns would acquire these in opposite order.
	peers := dungeonPartyInstanceMemberSessions(instance, 0)
	for _, member := range members {
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok {
			continue
		}
		receiver := identity.session
		if only != nil && receiver != only {
			continue
		}
		if deferPeers {
			errs = errors.Join(errs, s.queueDetachedPartyProjection(identity, state, func() error {
				return s.sendMemberGiveupSurvivorRosterAfterDepartureMode(state, instance, departingID, false, receiver)
			}))
			continue
		}
		current := s.currentPartyRetirementGuard(receiver, &state)
		if !current() {
			continue
		}
		active := false
		for _, peer := range peers {
			if peer.CharacterID == identity.character && peer.Session == receiver {
				active = true
				break
			}
		}
		if !active {
			errs = errors.Join(errs, s.sendRuntimePartyMemberRemoved(receiver, 0, state))
			continue
		}
		if departingID != 0 && len(members) == 1 {
			// Updating an active roster directly from two rows to one does not
			// retire the native party HUD/network's previous peer entries. This
			// is visible when a four-player party shrinks to its last fixed slot.
			// There are no surviving remote actors to preserve now: retire the
			// local client binding before restoring its server-owned singleton.
			// Keep the manager, fixed slot and dungeon instance unchanged.
			sent, err := s.resetDungeonSingletonPartyDisplay(receiver, state, instance, departingID, current)
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				continue
			}
		}
		owners := []byte{currentSceneObjectContext}
		if owner := currentClientSelectedChannel(receiver); owner != currentSceneObjectContext {
			owners = append([]byte{owner}, owners...)
		}
		complete := true
		for _, owner := range owners {
			sent, err := s.sendMemberGiveupSurvivorRosterInContext(receiver, state, departingID, owner, current)
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		// NOTI9 diffs the old manager slots to retire missing network peers.
		// NOTI6 clears those slots before erasing the actor. Sending it first
		// loses the only identity that the roster diff can use for removal.
		// Keep each receiver's actor until its complete roster has been sent.
		if departingID != 0 && current() {
			errs = errors.Join(errs, s.sendDungeonDepartedActorRemoval(receiver, departingID, "member_giveup_confirmed_town", state))
		}
		_, err := s.sendRetiredPartyPacket(receiver, 0x0099, runtimePartyRealtimeInfoBody(state), &state, current, false)
		errs = errors.Join(errs, err)
	}
	return errs
}

func (s *Service) resetDungeonSingletonPartyDisplay(receiver *gameSession, state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16, current func() bool) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || len(runtimePartyMembers(state)) != 1 {
		return false, nil
	}
	owners := []byte{currentSceneObjectContext}
	if owner := currentClientSelectedChannel(receiver); owner != currentSceneObjectContext {
		owners = append([]byte{owner}, owners...)
	}
	instance.mu.Lock()
	keys := instance.retiredPartyRecordKeys
	instance.mu.Unlock()
	ordered := []uint16{selectedCharacterID(receiver), departingID}
	for _, key := range keys {
		seen := key == 0
		for _, old := range ordered {
			seen = seen || old == key
		}
		if !seen {
			ordered = append(ordered, key)
		}
	}
	for _, key := range ordered {
		for _, owner := range owners {
			// The paired kind3 retirement helper preserves the living local
			// drawable. Empty kind2 would run the destructive ordinary slot setter.
			sent, err := s.sendRetiredPartyPacket(receiver, 9,
				buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(key), owner, s.currentSceneActorRoute()), &state, current, false)
			if err != nil || !sent {
				return false, err
			}
		}
	}
	return s.sendRetiredPartyPacket(receiver, 0x99, runtimePartyRealtimeInfoBody(alignedcmd.PartyState{}), &state, current, false)
}

func (s *Service) sendMemberGiveupSurvivorRosterInContext(receiver *gameSession, state alignedcmd.PartyState, departingID uint16, owner byte, current func() bool) (bool, error) {
	selection := currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerExplicit, Owner: owner}
	if departingID != 0 {
		// Every record can still own a survivor's party binding. Give the
		// departed actor's record the same retained roster, not an empty roster:
		// native kind2 would otherwise destroy all its former peers' models and
		// tear down the local network before the following kind0 can rebind it.
		sent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, receiver, state, "member_giveup_network_roster", nil, selection)
		if err != nil || !sent {
			return false, err
		}
		if !current() {
			return false, nil
		}
		sent, err = s.sendCurrentPartyRecordProjectionInContext(receiver, receiver, state, "member_giveup_retained_record", nil, selection, departingID)
		if err != nil || !sent {
			return false, err
		}
	}
	selfSent := false
	for _, row := range runtimePartyMembers(state) {
		actor, found := s.onlineGameSession(row.UserID)
		if !found {
			continue
		}
		sent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, "member_giveup_survivor_dungeon_roster", nil, selection)
		if err != nil || !sent {
			return false, err
		}
		selfSent = selfSent || actor == receiver
	}
	return selfSent, nil
}
