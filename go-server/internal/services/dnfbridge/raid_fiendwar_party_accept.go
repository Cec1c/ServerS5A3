package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Encounter participants are selected before dungeon entry, not by a new town
// invitation during combat. Return the native mode-bearing failure rather than
// silently dropping it at PeerInSameArea; this path never registers an invite.
func (s *Service) rejectCurrentFiendWarActivePartyRequest(p *gameSession, typ uint16, request party.RequestPeerRequest) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || !isCurrentFiendWarChannel(p) || (request.Mode != 0 && request.Mode != 12) || request.TargetID == 0 || request.TargetID == p.selectedCharacterID {
		return false, nil
	}
	p.dungeon.mu.Lock()
	active := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if !active {
		return false, nil
	}
	// v150 COMMAND10: 2523cf6/2523d4e consume success/error; 1fc2c05
	// consumes mode even on failure. Generic [0,error] would truncate this reader.
	if err := s.sendGameUpperRawClassCodec(p, typ, []byte{0, partyCommandFailureGeneric, request.Mode}, dnfproto.DefaultChannelClassification, true); err != nil {
		return true, err
	}
	refreshed := false
	if target, online := s.onlineGameSession(request.TargetID); online {
		if snapshot, valid := s.currentFiendWarExistingEncounterParty(p, target); valid {
			refreshed = s.sendDungeonPartyFrameRoster(snapshot.StateFor(p.selectedCharacterID), "fiendwar_active_party_request_rejected") > 0
		}
	}
	s.logGameEvent(p, "game-fiendwar-active-party-request-rejected", "type", typ,
		"char_id", p.selectedCharacterID, "target_id", request.TargetID, "mode", request.Mode,
		"failure_code", partyCommandFailureGeneric, "existing_roster_refreshed", refreshed,
		"reason", "encounter_membership_is_frozen_before_dungeon_entry")
	return true, nil
}

// An encounter can replace a town lobby before its invite response arrives.
// Create/leaveLocked deliberately retire that lobby's invitations. A response
// may acknowledge the already committed encounter membership, but cannot use
// the expired invitation to join, move, or rebuild any party.
func (s *Service) acknowledgeCurrentFiendWarPartyAccept(p, target *gameSession, typ uint16, response party.ResponsePeerRequest, hadInvite bool) (bool, error) {
	if (response.Mode != 0 && response.Mode != 12) || response.Value != 0 {
		return false, nil
	}
	snapshot, ok := s.currentFiendWarExistingEncounterParty(p, target)
	if !ok {
		return false, nil
	}
	if err := s.sendGameUpperSuccess(p, typ, party.BuildResponsePeerAckPayload(response.TargetID, response.Mode)); err != nil {
		return true, err
	}
	// The native class0/op11 is a formation handshake, not a refresh. Neither
	// that packet nor an op8 acceptance notice should restart an existing link.
	state := snapshot.StateFor(p.selectedCharacterID)
	s.sendDungeonPartyFrameRoster(state, "fiendwar_existing_encounter_accept")
	s.logGameEvent(p, "game-fiendwar-existing-party-accept-ack",
		"type", typ, "char_id", p.selectedCharacterID, "target_id", response.TargetID,
		"party_id", snapshot.ID, "had_pending_invite", hadInvite,
		"membership_mutation", "none", "peer_handshake", "not_replayed")
	return true, nil
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
