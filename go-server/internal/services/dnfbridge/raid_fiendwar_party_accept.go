package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// A combat request can repeat an encounter party already committed by entry.
// Restore that exact ready party without restarting the peer handshake. Other
// requests still need an encounter admission/transfer owner, not a town join.
func (s *Service) rejectCurrentFiendWarActivePartyRequest(p *gameSession, typ uint16, request party.RequestPeerRequest) (bool, error) {
	diagnostic := s.fiendWarPartyAdmissionDiagnosticEligible(p, request.Mode, request.TargetID)
	handled, err, reason := s.rejectCurrentFiendWarActivePartyRequestDecision(p, typ, request)
	if diagnostic {
		target, _ := s.onlineGameSession(request.TargetID)
		s.logFiendWarPartyAdmission("request", p, request.TargetID, target, request.Mode, reason)
	}
	return handled, err
}

func (s *Service) rejectCurrentFiendWarActivePartyRequestDecision(p *gameSession, typ uint16, request party.RequestPeerRequest) (bool, error, string) {
	if !s.currentFiendWarAutomaticPartyOnly(p) || (request.Mode != 0 && request.Mode != 12) {
		return false, nil, "fiendwar_request_scope_mismatch"
	}
	if target, online := s.onlineGameSession(request.TargetID); online {
		if snapshot, valid := s.currentFiendWarReadyEncounterParty(p, target); valid {
			if err := s.sendGameUpperRawClassCodec(p, typ, party.BuildRequestPeerAck(), dnfproto.DefaultChannelClassification, true); err != nil {
				return true, err, "existing_party_ack_write_failed"
			}
			s.sendDungeonPartyFrameRoster(snapshot.StateFor(p.selectedCharacterID), "fiendwar_existing_encounter_request")
			// The roster restores op153/op9 but does not recreate a peer actor that
			// the client dropped. Reuse the normal per-binding projection after the
			// same-run/all-ready checks above. Its claims make a repeated recovery
			// idempotent, while a failed direction releases only that claim so the
			// next request can finish it. Do not replay op11/op8 or touch membership.
			projection := "restored_or_already_projected"
			reason := "existing_shared_party_restored"
			if err := s.projectDungeonPartyPeerActors(p, "fiendwar_existing_encounter_request"); err != nil {
				projection = "write_failed"
				reason = "existing_party_peer_actor_write_failed"
				s.logGameEvent(p, "game-fiendwar-existing-party-peer-actor-failed",
					"type", typ, "char_id", p.selectedCharacterID, "target_id", request.TargetID,
					"party_id", snapshot.ID, "source", "fiendwar_existing_encounter_request",
					"error", err)
			}
			s.logGameEvent(p, "game-fiendwar-existing-party-request-ack", "type", typ,
				"char_id", p.selectedCharacterID, "target_id", request.TargetID, "mode", request.Mode,
				"party_id", snapshot.ID, "membership_mutation", "none", "peer_handshake", "not_replayed",
				"peer_actor_projection", projection)
			return true, nil, reason
		}
	}
	// v150 COMMAND10: 2523cf6/2523d4e consume success/error; 1fc2c05
	// consumes mode even on failure. Generic [0,error] would truncate this reader.
	if err := s.sendGameUpperRawClassCodec(p, typ, []byte{0, partyCommandFailureGeneric, request.Mode}, dnfproto.DefaultChannelClassification, true); err != nil {
		return true, err, "rejection_ack_write_failed"
	}
	refreshed := false
	reason := "peer_offline"
	if target, online := s.onlineGameSession(request.TargetID); online {
		reason = "no_shared_encounter_party"
		if snapshot, valid := s.currentFiendWarExistingEncounterParty(p, target); valid {
			refreshed = s.sendDungeonPartyFrameRoster(snapshot.StateFor(p.selectedCharacterID), "fiendwar_active_party_request_rejected") > 0
			reason = "shared_party_not_ready_or_scene_mismatch"
		}
	}
	s.logGameEvent(p, "game-fiendwar-active-party-request-rejected", "type", typ,
		"char_id", p.selectedCharacterID, "target_id", request.TargetID, "mode", request.Mode,
		"failure_code", partyCommandFailureGeneric, "existing_roster_refreshed", refreshed,
		"reason", "encounter_membership_is_frozen_before_dungeon_entry")
	return true, nil, reason
}

// The request restores only members already admitted to one encounter and
// projected into the same ready scene. A run key alone is not admission proof;
// revalidate each member against its runtime, reservation and current session.
func (s *Service) currentFiendWarReadyEncounterParty(p, target *gameSession) (party.RuntimePartySnapshot, bool) {
	snapshot, valid := s.currentFiendWarExistingEncounterParty(p, target)
	if !valid {
		return party.RuntimePartySnapshot{}, false
	}
	var first dungeonPartyRunKey
	for i, member := range snapshot.Members {
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.generation != member.SessionGeneration {
			return party.RuntimePartySnapshot{}, false
		}
		key, ready := s.currentDungeonPartyRunKey(peer.session)
		if !ready || key.PartyID != snapshot.ID || (i > 0 && key != first) {
			return party.RuntimePartySnapshot{}, false
		}
		if peer.session != p {
			if _, valid := s.currentFiendWarExistingEncounterParty(p, peer.session); !valid {
				return party.RuntimePartySnapshot{}, false
			}
		}
		peer.session.dungeon.mu.Lock()
		r := peer.session.dungeon.runtime
		valid := r != nil && r.Session != nil && peer.session.postStartMapPlayerStateSent
		if valid {
			scene, exists := r.Session.Scene()
			actual, found := dungeonPartyRunKeyFrom(peer.session, r, scene)
			valid = exists && found && actual == key
		}
		peer.session.dungeon.mu.Unlock()
		if !valid || !s.boundGameSessionCharacterCurrent(peer) {
			return party.RuntimePartySnapshot{}, false
		}
		first = key
	}
	return snapshot, true
}

// An encounter can replace a town lobby before its invite response arrives.
// Create/leaveLocked deliberately retire that lobby's invitations. A response
// may acknowledge the already committed encounter membership, but cannot use
// the expired invitation to join, move, or rebuild any party.
func (s *Service) acknowledgeCurrentFiendWarPartyAccept(p, target *gameSession, typ uint16, response party.ResponsePeerRequest, hadInvite bool) (bool, error) {
	diagnostic := s.fiendWarPartyAdmissionDiagnosticEligible(p, response.Mode, response.TargetID)
	handled, err, reason := s.acknowledgeCurrentFiendWarPartyAcceptDecision(p, target, typ, response, hadInvite)
	if diagnostic {
		s.logFiendWarPartyAdmission("accept", p, response.TargetID, target, response.Mode, reason, "had_invite", hadInvite)
	}
	return handled, err
}

func (s *Service) acknowledgeCurrentFiendWarPartyAcceptDecision(p, target *gameSession, typ uint16, response party.ResponsePeerRequest, hadInvite bool) (bool, error, string) {
	if (response.Mode != 0 && response.Mode != 12) || response.Value != 0 {
		return false, nil, "not_party_accept_response"
	}
	snapshot, ok := s.currentFiendWarExistingEncounterParty(p, target)
	if !ok {
		return false, nil, "no_existing_shared_encounter_party"
	}
	if err := s.sendGameUpperSuccess(p, typ, party.BuildResponsePeerAckPayload(response.TargetID, response.Mode)); err != nil {
		return true, err, "accept_ack_write_failed"
	}
	// The native class0/op11 is a formation handshake, not a refresh. Neither
	// that packet nor an op8 acceptance notice should restart an existing link.
	state := snapshot.StateFor(p.selectedCharacterID)
	s.sendDungeonPartyFrameRoster(state, "fiendwar_existing_encounter_accept")
	s.logGameEvent(p, "game-fiendwar-existing-party-accept-ack",
		"type", typ, "char_id", p.selectedCharacterID, "target_id", response.TargetID,
		"party_id", snapshot.ID, "had_pending_invite", hadInvite,
		"membership_mutation", "none", "peer_handshake", "not_replayed")
	return true, nil, "existing_shared_party_restored"
}

func (s *Service) currentFiendWarExistingEncounterParty(p, target *gameSession) (party.RuntimePartySnapshot, bool) {
	empty := party.RuntimePartySnapshot{}
	if s == nil || s.options.currentClientVersion != "official-24.9" || p == target || !isCurrentFiendWarChannel(p) || !isCurrentFiendWarChannel(target) || p.channel.ID != target.channel.ID {
		return empty, false
	}
	own, ownOK := s.boundGameSessionCharacterSnapshot(p)
	peer, peerOK := s.boundGameSessionCharacterSnapshot(target)
	if !ownOK || !peerOK {
		return empty, false
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(own.character, own.generation)
	other, otherFound := manager.SnapshotByUser(peer.character, peer.generation)
	if !found || !otherFound || snapshot.ID != other.ID || len(snapshot.Members) < 2 {
		return empty, false
	}
	ids := make([]uint16, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		current, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || current.generation != member.SessionGeneration {
			return empty, false
		}
		ids = append(ids, member.UserID)
	}
	// Take each dungeon lock separately, preserving the established lock order.
	// The normal raid validator checks account, channel, instance/attempt,
	// reservation, departure, expiry and the immutable admitted participants.
	var first currentRaidDungeonEntry
	var instance *dungeonPartyInstance
	for i, actor := range []*gameSession{p, target} {
		actor.dungeon.mu.Lock()
		r := actor.dungeon.runtime
		valid := r != nil && r.Session != nil && r.raidEntry != nil && r.partyInstance != nil && !r.townReturnPending && r.Session.Snapshot().Run.Status == worldmap.DungeonRunActive
		if valid {
			valid = s.validateCurrentRaidDungeonEntry(actor, r) == nil
		}
		if valid {
			valid = sameRaidParticipants(ids, r.raidEntry.Participants)
		}
		if valid && i == 0 {
			first = *r.raidEntry
			instance = r.partyInstance
		}
		if valid && i == 1 {
			valid = first.RaidKey == r.raidEntry.RaidKey && first.InstanceID == r.raidEntry.InstanceID && first.RunID == r.raidEntry.RunID && instance == r.partyInstance
		}
		actor.dungeon.mu.Unlock()
		if !valid {
			return empty, false
		}
	}
	current, currentOK := manager.SnapshotByUser(own.character, own.generation)
	latest, latestOK := manager.SnapshotByUser(peer.character, peer.generation)
	if !currentOK || !latestOK || current.ID != snapshot.ID || latest.ID != snapshot.ID || current.Leader != snapshot.Leader || len(current.Members) != len(snapshot.Members) || !s.boundGameSessionCharacterCurrent(own) || !s.boundGameSessionCharacterCurrent(peer) {
		return empty, false
	}
	for i, member := range current.Members {
		old := snapshot.Members[i]
		if member.UserID != old.UserID || member.SessionGeneration != old.SessionGeneration || member.Slot != old.Slot {
			return empty, false
		}
	}
	return snapshot, true
}
