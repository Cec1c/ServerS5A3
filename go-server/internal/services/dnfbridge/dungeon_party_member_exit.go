package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// A member's explicit mid-run giveup leaves the party after the town scene
// is confirmed. Completion and automatic deadline returns never call this.
// The remaining members retain their dungeon runtime and shared instance.
func (s *Service) leaveRuntimePartyOnMemberGiveup(session *gameSession, instance *dungeonPartyInstance) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !s.boundGameSessionCharacterCurrent(identity) || instance == nil {
		return nil
	}
	manager := s.runtimePartyManagerForService()
	before, found := manager.SnapshotByUser(identity.character, identity.generation)
	drifted := !found
	if drifted {
		// Only the current authoritative socket may repair recorded generation
		// drift. An old dungeon return must never remove a new party membership.
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		before, found = manager.SnapshotByUser(identity.character, 0)
	}
	if !found || before.ID != instance.key.PartyID || before.Leader == identity.character {
		return nil
	}
	peers := dungeonPartyInstanceMemberSessions(instance, identity.character)
	var next alignedcmd.PartyState
	var removed bool
	if s.options.currentClientVersion == "official-24.9" {
		// The leader is still inside this exact instance. Do not pass through
		// the generic below-two-members publisher: it erases the surviving
		// lobby's projection and recycles its still-active dungeon registry.
		var recordedGeneration uint64
		for _, member := range before.Members {
			if member.UserID == identity.character {
				recordedGeneration = member.SessionGeneration
			}
		}
		var result party.RuntimePartyResult
		if !s.withCurrentDungeonPartyRetirement(identity, instance, func() bool {
			result = manager.LeaveExpected(identity.character, recordedGeneration, before.ID)
			return result.OK
		}) {
			return nil
		}
		removed = result.OK
		if removed {
			s.logRuntimePartyMembershipRemoved("member_giveup_confirmed_town", identity.character, identity.generation, before, result)
			s.clearRuntimePartyProjection(identity.character, identity.generation)
			next = s.publishRuntimePartySnapshot(result.Party)
		}
	} else if drifted {
		next, _, removed = s.repairManagedRuntimePartyLeave(identity)
	} else {
		next, _, removed = s.leaveManagedRuntimeParty(identity, "member_giveup_confirmed_town")
	}
	if !removed {
		return nil
	}
	var errs error
	if s.options.currentClientVersion == "official-24.9" {
		errs = errors.Join(errs, s.clearDisbandedDungeonPartyRecords(session, before.StateFor(before.Leader).Members, before.Leader, false, "member_giveup_retired_receiver"))
	}
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
	errs = errors.Join(errs, s.sendRuntimePartyMemberRemoved(session, identity.character, alignedcmd.PartyState{}))
	if s.options.currentClientVersion == "official-24.9" {
		errs = errors.Join(errs, s.sendMemberGiveupSurvivorRosterAfterDeparture(next, instance, identity.character))
	} else {
		errs = errors.Join(errs, s.broadcastRuntimePartyMemberRemoved(before.StateFor(before.Leader), next, identity.character, session))
	}
	s.logGameEvent(session, "game-party-member-giveup-left", "char_id", identity.character,
		"party_id", before.ID, "leader_char_id", before.Leader, "remaining_members", len(next.Members))
	return errs
}

// Rebuild only the remaining records in their current scene context. A town
// owner record cannot replace a survivor's old owner-0 dungeon roster.
func (s *Service) sendMemberGiveupSurvivorRoster(state alignedcmd.PartyState, instance *dungeonPartyInstance) error {
	return s.sendMemberGiveupSurvivorRosterAfterDeparture(state, instance, 0)
}

func (s *Service) sendMemberGiveupSurvivorRosterAfterDeparture(state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16) error {
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
			// The server keeps this player's active instance and fixed slot, but
			// a one-player dungeon has no remote party HUD/network membership.
			// Retire both old records, self last, before NOTI6 erases the peer
			// identity. Requires the client local-actor-preserving kind3 helper:
			// deleting the local party record must not destroy its living model.
			retired := []alignedcmd.PartyMemberState{{UserID: departingID}, member}
			if err := s.clearDungeonSurvivorTownPartyRecords(receiver, retired, "member_giveup_single_survivor", state); err != nil {
				errs = errors.Join(errs, err)
				continue
			}
			if err := s.clearDisbandedDungeonPartyRecords(receiver, retired, 0, false, "member_giveup_single_survivor", state); err != nil {
				errs = errors.Join(errs, err)
				continue
			}
			if !current() {
				continue
			}
			if err := s.sendDungeonDepartedActorRemoval(receiver, departingID, "member_giveup_single_survivor", state); err != nil {
				errs = errors.Join(errs, err)
				continue
			}
			_, err := s.sendRetiredPartyPacket(receiver, 0x0099, runtimePartyRealtimeInfoBody(alignedcmd.PartyState{}), &state, current, false)
			errs = errors.Join(errs, err)
			continue
		}
		if departingID != 0 {
			// First apply the new membership to this receiver's local record,
			// while the old actor slots still identify every network peer.
			sent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, receiver, state, "member_giveup_network_roster", nil, currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerDungeonContext})
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				continue
			}
			body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(departingID), currentSceneObjectContext, s.currentSceneActorRoute())
			sent, err = s.sendRetiredPartyPacket(receiver, uint16(dnfenum.CmdPacketRecoverStamina), body, &state, current, false)
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				continue
			}
		}
		var rosterErr error
		selfSent := false
		for _, actorRow := range runtimePartyMembers(state) {
			actor, found := s.onlineGameSession(actorRow.UserID)
			if !found {
				continue
			}
			sent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, "member_giveup_survivor_dungeon_roster", nil, currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerDungeonContext})
			selfSent = selfSent || (sent && actor == receiver)
			rosterErr = errors.Join(rosterErr, err)
		}
		errs = errors.Join(errs, rosterErr)
		// NOTI9 diffs the old manager slots to retire missing network peers.
		// NOTI6 clears those slots before erasing the actor. Sending it first
		// loses the only identity that the roster diff can use for removal.
		// Keep each receiver's actor until its complete roster has been sent.
		if rosterErr == nil && selfSent && departingID != 0 && current() {
			errs = errors.Join(errs, s.sendDungeonDepartedActorRemoval(receiver, departingID, "member_giveup_confirmed_town", state))
		}
		_, err := s.sendRetiredPartyPacket(receiver, 0x0099, runtimePartyRealtimeInfoBody(state), &state, current, false)
		errs = errors.Join(errs, err)
	}
	return errs
}
