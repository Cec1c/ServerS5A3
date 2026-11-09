package dnfbridge

import (
	"strconv"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentWeddingClearProof struct {
	partner, reason                      string
	partyID                              uint16
	instanceID, transportGeneration      uint64
	canonicalGeneration, partyGeneration uint64
	partyMembers, instanceMembers        int
}

type currentWeddingClearTransport struct {
	transport, canonical boundGameSessionCharacter
	account              string
}

// Party membership is character-scoped, as in ordinary dungeon admission.
// A historic party-member generation is not the identity of a current socket.
// Pin the actual transport and current canonical owner separately instead.
func (s *Service) currentWeddingClearTransport(session *gameSession, account string) (currentWeddingClearTransport, bool) {
	if s == nil || session == nil || account == "" {
		return currentWeddingClearTransport{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, generation := session.selectedCharacterID, session.characterGeneration
	canonical := s.gameSessions[id]
	if id == 0 || generation == 0 || canonical == nil || canonical.selectedCharacterID != id || canonical.characterGeneration == 0 ||
		session.loginRevoked.Load() || canonical.loginRevoked.Load() ||
		s.accountIDForSession(session) != account || s.accountIDForSession(canonical) != account ||
		(session != canonical && (session.accountID == "" || canonical.accountID == "" || session.connID == "" || s.liveGameSessions[session.connID] != session)) {
		return currentWeddingClearTransport{}, false
	}
	return currentWeddingClearTransport{
		transport: boundGameSessionCharacter{session: session, character: id, generation: generation},
		canonical: boundGameSessionCharacter{session: canonical, character: id, generation: canonical.characterGeneration},
		account:   account,
	}, true
}

func (s *Service) currentWeddingClearTransportCurrent(pin currentWeddingClearTransport) bool {
	current, valid := s.currentWeddingClearTransport(pin.transport.session, pin.account)
	return valid && current == pin
}

// Called with the source dungeon lock. Read the shared registry without
// acquiring the partner's session lock; the quest owner rechecks both links.
func (s *Service) currentWeddingClearPartner(session *gameSession, runtime *runtimeDungeonState) string {
	return s.currentWeddingClearPartnerProof(session, runtime).partner
}

func (s *Service) currentWeddingClearPartnerProof(session *gameSession, runtime *runtimeDungeonState) currentWeddingClearProof {
	proof := currentWeddingClearProof{reason: "not_two_member_dungeon_run"}
	if session == nil || runtime == nil || runtime.partyInstance == nil || runtime.entryPartyCount != 2 || runtime.Dungeon.ID == 3541 ||
		session.dungeon.runtime != runtime || runtime.Session == nil {
		return proof
	}
	self, valid := s.currentWeddingClearTransport(session, runtime.AccountID)
	proof.reason = "source_transport_owner_unavailable"
	if !valid || !dungeonRuntimeOwnsCharacter(runtime, self.transport.character) || runtime.Character.AccountID != self.account {
		return proof
	}
	proof.transportGeneration, proof.canonicalGeneration = self.transport.generation, self.canonical.generation
	instance := runtime.partyInstance
	proof.instanceID = instance.id
	var peerID uint16
	var peerSession *gameSession
	var peerRuntime *runtimeDungeonState
	var peerScene *worldmap.DungeonSession
	// The attachment ledger is the cross-session run owner. Never acquire a
	// peer dungeon lock or read its mutable dungeon.runtime/partyInstance.
	registry := s.partyDungeonInstanceRegistryForService()
	registry.mu.Lock()
	instance.mu.Lock()
	proof.instanceMembers, proof.partyID = len(instance.members), instance.key.PartyID
	_, selfMember := instance.members[self.transport.character]
	attached := registry.byKey[instance.key] == instance && selfMember && proof.instanceMembers == 2 && instance.sessions[self.transport.character] == session &&
		instance.memberRuntimes[self.transport.character] == runtime && instance.scenes[self.transport.character] == runtime.Session
	if attached {
		for id := range instance.members {
			if id != self.transport.character {
				peerID, peerSession, peerRuntime, peerScene = id, instance.sessions[id], instance.memberRuntimes[id], instance.scenes[id]
			}
		}
	}
	instance.mu.Unlock()
	registry.mu.Unlock()
	proof.reason = "shared_run_attachment_unavailable"
	if !attached || peerID == 0 || peerSession == nil || peerRuntime == nil || peerScene == nil || peerRuntime.Session != peerScene {
		return proof
	}
	other, valid := s.currentWeddingClearTransport(peerSession, peerRuntime.AccountID)
	proof.reason = "partner_transport_owner_unavailable"
	if !valid || other.transport.character != peerID || self.account == other.account {
		return proof
	}
	manager := s.runtimePartyManagerForService()
	party, joined := manager.SnapshotByUser(self.transport.character, 0)
	proof.partyMembers = len(party.Members)
	proof.reason = "current_two_member_party_unavailable"
	if !joined || party.ID != proof.partyID || len(party.Members) != 2 {
		return proof
	}
	foundPeer := false
	for _, member := range party.Members {
		if member.UserID == self.transport.character {
			proof.partyGeneration = member.SessionGeneration
		}
		foundPeer = foundPeer || member.UserID == peerID
	}
	if !foundPeer {
		return proof
	}
	peerRun := peerScene.Snapshot().Run
	proof.reason = "partner_dungeon_run_mismatch"
	if peerRun.DungeonID != runtime.Dungeon.ID || peerRun.MazeIndex != runtime.MazeIndex || peerRun.Status == worldmap.DungeonRunAbandoned {
		return proof
	}
	after, stillJoined := manager.SnapshotByUser(self.transport.character, 0)
	proof.reason = "party_or_transport_changed"
	if !stillJoined || !sameCurrentPartyProjectionMembership(party, after) ||
		!s.currentWeddingClearTransportCurrent(self) || !s.currentWeddingClearTransportCurrent(other) {
		return proof
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	instance.mu.Lock()
	defer instance.mu.Unlock()
	_, selfMember = instance.members[self.transport.character]
	_, peerMember := instance.members[peerID]
	proof.reason = "shared_run_attachment_changed"
	if registry.byKey[instance.key] != instance || !selfMember || !peerMember || len(instance.members) != 2 || instance.sessions[self.transport.character] != session ||
		instance.memberRuntimes[self.transport.character] != runtime || instance.scenes[self.transport.character] != runtime.Session ||
		instance.sessions[peerID] != peerSession || instance.memberRuntimes[peerID] != peerRuntime || instance.scenes[peerID] != peerScene {
		return proof
	}
	proof.partner, proof.reason = strconv.Itoa(int(peerID)), "current_two_member_shared_run"
	return proof
}

// Only the active wedding clear quest calls this, once at its Phase-A barrier.
// Keep it visible with packet logging disabled without names, accounts or raw errors.
func (s *Service) logCurrentWeddingClearDiagnostic(session *gameSession, runtime *runtimeDungeonState, proof currentWeddingClearProof, before, after int64, outcome string) {
	s.logInfo("dnfbridge wedding clear diagnostic", "kind", "game-wedding-clear-phase-a", "conn_id", session.connID,
		"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "party_id", proof.partyID,
		"instance_id", proof.instanceID, "party_member_count", proof.partyMembers, "instance_member_count", proof.instanceMembers,
		"transport_generation", proof.transportGeneration, "canonical_generation", proof.canonicalGeneration, "party_member_generation", proof.partyGeneration,
		"partner_id", proof.partner, "proof_reason", proof.reason, "remaining_before", before, "remaining_after", after,
		"outcome", outcome, "source", "authoritative_completed_run_phase_a", "reward_granted", false)
}
