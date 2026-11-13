package dnfbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
)

const dungeonPeerNativeRegistrationOpcode uint16 = 0x7f14

// MembershipRevision, both session bindings, both placement epochs and the
// exact shared instance fence even an ABA reuse of the numeric room/run key.
func (s *Service) bindDungeonPeerNativeMembership(p *dungeonPeerActorRecovery) {
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(p.receiver.character, p.receiver.generation)
	if !found {
		return
	}
	state := snapshot.StateFor(p.receiver.character)
	guard := s.currentPartyProjectionMembershipGuard(p.receiver, &state)
	p.membershipCurrent = func() bool {
		now, ok := manager.SnapshotByUser(p.receiver.character, p.receiver.generation)
		return ok && now.MembershipRevision == snapshot.MembershipRevision && guard()
	}
}

func (s *Service) sendDungeonPeerNativeRegistrationQuery(p *dungeonPeerActorRecovery) error {
	if p.membershipCurrent == nil || !p.membershipCurrent() {
		return nil
	}
	snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(p.receiver.character, p.receiver.generation)
	if !found {
		return nil
	}
	var q [64]byte
	copy(q[:], "ARQ1")
	q[4] = 1
	if _, err := rand.Read(q[8:24]); err != nil {
		return err
	}
	self, peer := false, false
	for _, m := range snapshot.Members {
		if m.Slot >= 8 || m.UserID == 0 || binary.LittleEndian.Uint16(q[24+int(m.Slot)*2:]) != 0 {
			return nil
		}
		binary.LittleEndian.PutUint16(q[24+int(m.Slot)*2:], m.UserID)
		if m.UserID == p.receiver.character && m.SessionGeneration == p.receiver.generation {
			q[5], self = m.Slot, true
		}
		if m.UserID == p.actor.character && m.SessionGeneration == p.actor.generation {
			q[6], peer = m.Slot, true
		}
	}
	remaining := p.deadline.Sub(s.ensureGameplayTimeQueue().Now()).Milliseconds()
	if !self || !peer || remaining <= 0 || s.dungeonPeerActorRecoveryStatus(p) != dungeonPeerRecoveryReady {
		return nil
	}
	binary.LittleEndian.PutUint32(q[40:], uint32(p.key.Run.MapID))
	binary.LittleEndian.PutUint32(q[44:], p.key.Run.Seed)
	binary.LittleEndian.PutUint64(q[48:], p.receiverEpoch)
	binary.LittleEndian.PutUint32(q[56:], uint32(snapshot.MembershipRevision))
	binary.LittleEndian.PutUint32(q[60:], uint32(min(remaining, int64(15000))))
	receiver := p.receiver.session
	receiver.dungeonPartyRunMu.Lock()
	if receiver.dungeonPeerRecovery[p.key] != p || p.nativeRegistered {
		receiver.dungeonPartyRunMu.Unlock()
		return nil
	}
	p.nativeQuery = q
	receiver.dungeonPartyRunMu.Unlock()
	return s.sendGameUpperFixed16Transport(receiver, dungeonPeerNativeRegistrationOpcode, q[:], 1, 0, false, "party_native_registration_query_v1")
}

// The decoded native result reports membership/type/registry of this peer in
// the client's pinned current room. It is not proof of rendering or future UDP
// health, and cannot change eligibility, monsters, skills or entry admission.
func (s *Service) handleDungeonPeerNativeRegistrationReply(session *gameSession, body []byte) error {
	if session == nil || len(body) != 80 || string(body[:4]) != "ARR1" || body[64] < 1 || body[64] > 7 || body[65] != 0 || body[66] != 0 || body[67] != 0 {
		return nil
	}
	query := append([]byte(nil), body[:64]...)
	query[2] = 'Q'
	var p *dungeonPeerActorRecovery
	session.dungeonPartyRunMu.Lock()
	for _, candidate := range session.dungeonPeerRecovery {
		if !candidate.nativeRegistered && candidate.nativeQuery[0] != 0 && bytes.Equal(candidate.nativeQuery[:], query) {
			p = candidate
			break
		}
	}
	session.dungeonPartyRunMu.Unlock()
	if p == nil || p.membershipCurrent == nil || s.dungeonPeerActorRecoveryStatus(p) != dungeonPeerRecoveryReady {
		return nil
	}
	status := body[64]
	if status == 5 {
		if binary.LittleEndian.Uint32(body[68:]) == 0 || binary.LittleEndian.Uint32(body[72:]) == 0 || binary.LittleEndian.Uint32(body[76:]) == 0 || binary.LittleEndian.Uint32(body[76:]) > 60 {
			return nil
		}
		session.dungeonPartyRunMu.Lock()
		if session.dungeonPeerRecovery[p.key] == p && bytes.Equal(p.nativeQuery[:], query) {
			p.nativeRegistered = true
		}
		session.dungeonPartyRunMu.Unlock()
	}
	s.logGameEvent(session, "game-dungeon-party-peer-native-registration", "actor_char_id", p.actor.character, "native_status", status, "native_epoch", binary.LittleEndian.Uint32(body[68:]), "attempts", binary.LittleEndian.Uint32(body[76:]), "renderer_result", "unknown")
	return nil
}
