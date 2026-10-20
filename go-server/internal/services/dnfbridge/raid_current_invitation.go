package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/raid"
	"time"
)

const currentRaidPeerCheckOp uint16 = 889

type currentRaidInvitation struct {
	raidKey   uint32
	raidOwner *runtimeRaidState
	inviter   boundGameSessionCharacter
	recipient boundGameSessionCharacter
	expires   time.Time
}

// Actual player menu135 sends op10/mode10 from a leader to an unassigned
// target. The same mode sent by an unassigned source remains an application.
// The cross-context op820/handoff invitation contract is not enabled here.
func (s *Service) handleCurrentRaidInvitationCommand(session *gameSession, op uint16, body []byte) (bool, error) {
	if op == currentRaidPeerCheckOp {
		return true, s.handleCurrentRaidPeerCheck(session, op, body)
	}
	switch dnfenum.CmdPacket(op) {
	case dnfenum.CmdPacketRequestPeer:
		req, err := party.DecodeRequestPeerRequest(body)
		if err != nil {
			// A recognizable current mode10 from an actual raid member must
			// not silently fall through after a strict length rejection.
			if len(body) > 2 && body[2] == 10 {
				s.raidMu.Lock()
				owned := s.runtimeRaidByMemberLocked(selectedCharacterID(session)) != nil
				s.raidMu.Unlock()
				if owned {
					return true, s.sendRaidCommandFailure(session, op)
				}
			}
			return false, nil
		}
		if req.Mode != 10 {
			return false, nil
		}
		s.raidMu.Lock()
		state := s.runtimeRaidByMemberLocked(selectedCharacterID(session))
		owned := state != nil
		s.raidMu.Unlock()
		if !owned {
			return false, nil
		}
		return true, s.handleCurrentRaidInvitation(session, op, req)
	case dnfenum.CmdPacketResponsePeer:
		req, err := party.DecodeResponsePeerRequest(body)
		if err != nil || req.Mode != 10 {
			return false, nil
		}
		s.raidMu.Lock()
		_, pending := s.currentRaidInvitations[selectedCharacterID(session)]
		s.raidMu.Unlock()
		if !pending {
			return false, nil
		}
		return true, s.handleCurrentRaidInvitationAcceptance(session, op, req)
	default:
		return false, nil
	}
}

// The native op7 handler resolves the inviter with 296D730, so the authority
// and actual current scene bucket must agree. A replacement connection must
// not inherit either an old presence row or a pending capability.
func (s *Service) currentRaidInvitationPeers(source, target boundGameSessionCharacter) bool {
	a, b := source.session, target.session
	if a == nil || b == nil || source.character == target.character || !s.boundGameSessionCharacterCurrent(source) || !s.boundGameSessionCharacterCurrent(target) || a.channel.ID <= 0 || a.channel.ID > 255 || a.channel.ID != b.channel.ID || a.channel.Type != b.channel.Type || s.onlinePlayers == nil {
		return false
	}
	if _, ok := currentRaidRouteForChannel(int(a.channel.Type)); !ok {
		return false
	}
	ai, af := s.onlinePlayers.PlayerForCharacter(source.character)
	bi, bf := s.onlinePlayers.PlayerForCharacter(target.character)
	return af && bf && ai.Session == a && bi.Session == b && s.onlinePlayers.PeerInSameArea(source.character, target.character)
}
func (s *Service) handleCurrentRaidPeerCheck(session *gameSession, op uint16, body []byte) error {
	req, err := raid.DecodeCurrentPeerCheckRequest(body)
	if err != nil || !s.currentRaidAuthenticated(session) || session.channel.ID <= 0 || session.channel.ID > 255 || s.channelAdvertiseID() < 0 || s.channelAdvertiseID() > 255 || req.Server != byte(s.channelAdvertiseID()) || req.Channel != byte(session.channel.ID) {
		return s.sendRaidCommandFailure(session, op)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	target, online := s.onlineGameSessionCharacterSnapshot(req.TargetCharKey)
	if !bound || !online || !s.currentRaidAuthenticated(target.session) || !s.currentRaidInvitationPeers(source, target) {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	targetRaid := s.runtimeRaidByMemberLocked(target.character)
	valid := targetRaid == nil && s.currentRaidInvitationPeers(source, target)
	s.raidMu.Unlock()
	if !valid {
		return s.sendRaidCommandFailure(session, op)
	}
	return s.sendGameUpperRawClassCodec(session, op, raid.BuildCurrentUnassignedPeerCheckACK(), 1, true)
}
func (s *Service) currentRaidInvitationOpenLocked(state *runtimeRaidState, inviter, recipient boundGameSessionCharacter) bool {
	route, supported := currentRaidRouteForChannel(int(inviter.session.channel.Type))
	return state != nil && state.CurrentLayout && state.LeaderID == inviter.character && !state.Started && state.CurrentUIFlags[0] == 0 && supported && state.RouteOrRaidType == route && byte(state.RaidKey>>16) == byte(inviter.session.channel.ID) && len(state.Members) < currentRaidMemberMaximum(*state) && s.runtimeRaidByMemberLocked(recipient.character) == nil && s.currentRaidInvitationPeers(inviter, recipient)
}
func (s *Service) handleCurrentRaidInvitation(session *gameSession, op uint16, req party.RequestPeerRequest) error {
	if !s.currentRaidAuthenticated(session) || req.Value2 != 0 {
		return s.sendRaidCommandFailure(session, op)
	}
	inviter, bound := s.boundGameSessionCharacterSnapshot(session)
	recipient, online := s.onlineGameSessionCharacterSnapshot(req.TargetID)
	if !bound || !online || !s.currentRaidAuthenticated(recipient.session) || !s.currentRaidInvitationPeers(inviter, recipient) {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(inviter.character)
	if !s.currentRaidInvitationOpenLocked(state, inviter, recipient) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	if s.currentRaidInvitations == nil {
		s.currentRaidInvitations = make(map[uint16]currentRaidInvitation)
	}
	now := time.Now()
	for id, pending := range s.currentRaidInvitations {
		if !pending.expires.After(now) {
			delete(s.currentRaidInvitations, id)
		}
	}
	pending := currentRaidInvitation{raidKey: state.RaidKey, raidOwner: state, inviter: inviter, recipient: recipient, expires: now.Add(2 * time.Minute)}
	// Hold the identity directory through capability registration so a
	// concurrent rebind cannot cross the final generation check.
	s.mu.Lock()
	if !s.currentRaidInvitationIdentityLocked(inviter) || !s.currentRaidInvitationIdentityLocked(recipient) {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	s.currentRaidInvitations[recipient.character] = pending
	s.mu.Unlock()
	s.raidMu.Unlock()
	// Real class0/op7 mode10 consumes source/u8/u32 and three u16 values.
	// The last two u16 values are the sender's verified zero tail, not raid ID.
	if err := s.sendGameUpperRawClassCodec(recipient.session, 7, party.BuildRequestPeerNotice(inviter.character, req), 0, true); err != nil {
		s.raidMu.Lock()
		if s.currentRaidInvitations[recipient.character] == pending {
			delete(s.currentRaidInvitations, recipient.character)
		}
		s.raidMu.Unlock()
		return err
	}
	s.logGameEvent(session, "game-current-raid-invitation-pending", "raid_key", pending.raidKey, "inviter", inviter.character, "recipient", recipient.character)
	return s.sendGameUpperRawClassCodec(session, op, []byte{1}, 1, true)
}
func (s *Service) handleCurrentRaidInvitationAcceptance(session *gameSession, op uint16, req party.ResponsePeerRequest) error {
	// Only the actual observed accepting value0 is admitted. Unknown rejection
	// business values neither join nor consume the pending invitation.
	if !s.currentRaidAuthenticated(session) || req.Value != 0 {
		return s.sendRaidCommandFailure(session, op)
	}
	recipient, bound := s.boundGameSessionCharacterSnapshot(session)
	inviter, online := s.onlineGameSessionCharacterSnapshot(req.TargetID)
	if !bound || !online || !s.currentRaidAuthenticated(inviter.session) || !s.currentRaidInvitationPeers(inviter, recipient) {
		return s.sendRaidCommandFailure(session, op)
	}
	row, available := s.currentRaidApplicantRecord(session, raid.CurrentOtherChannelJoinRequest{})
	if !available {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	pending, found := s.currentRaidInvitations[recipient.character]
	state := s.raids[pending.raidKey]
	if !found || state != pending.raidOwner || pending.inviter != inviter || pending.recipient != recipient || !pending.expires.After(time.Now()) || !s.currentRaidInvitationOpenLocked(state, inviter, recipient) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	// raidMu protects capacity and membership; mu protects both selected
	// identities through the commit. Lock order is raidMu then mu, matching
	// the existing raid owner generation checks.
	s.mu.Lock()
	if !s.currentRaidInvitationIdentityLocked(inviter) || !s.currentRaidInvitationIdentityLocked(recipient) {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	placedMember, placed := appendRuntimeRaidMemberWithLegalGroup(state, runtimeRaidMemberState{CharID: row.CharKey, Name: row.Name, Level: uint32(row.Level), Job: row.Job, GrowType: row.Grow, UserState: 1, HPPercent: 100, MPPercent: 100})
	if !placed {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	delete(s.currentRaidInvitations, recipient.character)
	s.mu.Unlock()
	snapshot := cloneRuntimeRaidState(*state)
	s.raidMu.Unlock()
	if err := s.sendGameUpperRawClassCodec(session, op, append([]byte{1}, party.BuildResponsePeerAckPayload(req.TargetID, 10)...), 1, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(session, snapshot, 2); err != nil {
		return err
	}
	if err := s.broadcastRuntimeRaidRefresh(snapshot); err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-raid-invitation-accepted", "raid_key", snapshot.RaidKey, "inviter", inviter.character, "recipient", recipient.character, "member_count", len(snapshot.Members), "group_index", placedMember.GroupIndex, "slot_order", placedMember.SlotOrder)
	return nil
}

// Caller holds Service.mu. This predicate is used only while committing the
// invitation capability/member alongside raidMu, not as an unlocked snapshot.
func (s *Service) currentRaidInvitationIdentityLocked(identity boundGameSessionCharacter) bool {
	return identity.session != nil && identity.character != 0 && s.gameSessions[identity.character] == identity.session && identity.session.selectedCharacterID == identity.character && identity.session.characterGeneration == identity.generation
}
