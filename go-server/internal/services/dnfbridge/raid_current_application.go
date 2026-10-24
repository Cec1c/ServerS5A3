package dnfbridge

import (
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/raid"
)

type currentRaidApplication struct {
	raidKey   uint32
	applicant boundGameSessionCharacter
	leader    boundGameSessionCharacter
	expires   time.Time
}

// Current cross-channel UI also uses op820 when both players are on the same
// channel. Admission here is deliberately scoped to that verified case;
// cross-channel approval needs its native channel-handoff owner first.
func (s *Service) handleCurrentRaidApplicationCommand(session *gameSession, op uint16, body []byte) (bool, error) {
	switch dnfenum.CmdPacket(op) {
	case dnfenum.CmdPacketRaidOtherChannelRequestJoin:
		req, err := raid.DecodeCurrentOtherChannelJoinRequest(body)
		if err != nil {
			return true, s.sendRaidCommandFailure(session, op)
		}
		return true, s.handleCurrentRaidApplication(session, op, req)
	case dnfenum.CmdPacketRequestPeer:
		req, err := party.DecodeRequestPeerRequest(body)
		if err != nil || req.Mode != 10 {
			return false, nil
		}
		if session == nil {
			return true, s.sendRaidCommandFailure(session, op)
		}
		return true, s.handleCurrentRaidApplication(session, op, raid.CurrentOtherChannelJoinRequest{TargetServer: byte(s.channelAdvertiseID()), TargetCharKey: req.TargetID, TargetChannel: byte(session.channel.ID), ClientValue: req.Value0, NativeValue: req.Value1})
	case dnfenum.CmdPacketResponsePeer:
		req, err := party.DecodeResponsePeerRequest(body)
		if err != nil || req.Mode != 10 {
			return false, nil
		}
		return true, s.handleCurrentRaidApproval(session, op, req)
	default:
		return false, nil
	}
}

func (s *Service) currentRaidApplicantRecord(session *gameSession, req raid.CurrentOtherChannelJoinRequest) (raid.CurrentApplicationRecord, bool) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return raid.CurrentApplicationRecord{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(selectedCharacterID(session))))
	if err != nil || !found || c.AccountID != s.accountIDForSession(session) || c.Level <= 0 || c.Level > 65535 {
		return raid.CurrentApplicationRecord{}, false
	}
	return raid.CurrentApplicationRecord{CharKey: selectedCharacterID(session), Server: byte(s.channelAdvertiseID()), Channel: byte(session.channel.ID), Name: c.Name, Level: uint16(c.Level), Job: byte(numericCharacterStat(c.Job)), Grow: characterGrowTypeByte(c), ClientValue: req.ClientValue, NativeValue: req.NativeValue}, true
}

func (s *Service) handleCurrentRaidApplication(session *gameSession, op uint16, req raid.CurrentOtherChannelJoinRequest) error {
	if !s.currentRaidAuthenticated(session) || session.channel.ID <= 0 || session.channel.ID > 255 || s.channelAdvertiseID() < 0 || s.channelAdvertiseID() > 255 || req.TargetServer != byte(s.channelAdvertiseID()) || req.TargetChannel != byte(session.channel.ID) || req.TargetCharKey == selectedCharacterID(session) {
		return s.sendRaidCommandFailure(session, op)
	}
	applicant, bound := s.boundGameSessionCharacterSnapshot(session)
	leader, online := s.onlineGameSessionCharacterSnapshot(req.TargetCharKey)
	if !bound || !online || leader.session == nil || !s.currentRaidAuthenticated(leader.session) || leader.session.channel.ID != session.channel.ID || leader.session.channel.Type != session.channel.Type {
		return s.sendRaidCommandFailure(session, op)
	}
	row, found := s.currentRaidApplicantRecord(session, req)
	notice, err := raid.BuildCurrentRaidApplicationBody(row)
	if !found || err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	target := s.runtimeRaidByMemberLocked(leader.character)
	route, supported := currentRaidRouteForChannel(int(session.channel.Type))
	if target == nil || !target.CurrentLayout || target.LeaderID != leader.character || target.Started || target.CurrentUIFlags[0] != 0 || !supported || target.RouteOrRaidType != route || byte(target.RaidKey>>16) != req.TargetChannel || len(target.Members) >= currentRaidMemberMaximum(*target) || s.runtimeRaidByMemberLocked(applicant.character) != nil || !s.boundGameSessionCharacterCurrent(applicant) || !s.boundGameSessionCharacterCurrent(leader) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	if s.currentRaidApplications == nil {
		s.currentRaidApplications = make(map[uint16]currentRaidApplication)
	}
	now := time.Now()
	for id, pending := range s.currentRaidApplications {
		if !pending.expires.After(now) {
			delete(s.currentRaidApplications, id)
		}
	}
	pending := currentRaidApplication{raidKey: target.RaidKey, applicant: applicant, leader: leader, expires: now.Add(2 * time.Minute)}
	s.currentRaidApplications[applicant.character] = pending
	s.raidMu.Unlock()
	if err := s.sendGameUpperRawClassCodec(leader.session, raid.CurrentRaidApplicationMsgID, notice, 0, true); err != nil {
		s.raidMu.Lock()
		if s.currentRaidApplications[applicant.character] == pending {
			delete(s.currentRaidApplications, applicant.character)
		}
		s.raidMu.Unlock()
		return err
	}
	// Current 01FC373E (op10) and 01FC2430 (op820) success branches
	// consume no additional body. Do not append historical peer-ACK tails.
	ack := []byte{1}
	s.logGameEvent(session, "game-current-raid-application-pending", "raid_key", pending.raidKey, "applicant", applicant.character, "leader", leader.character, "notification", raid.CurrentRaidApplicationMsgID)
	return s.sendGameUpperRawClassCodec(session, op, ack, 1, true)
}

func (s *Service) handleCurrentRaidApproval(session *gameSession, op uint16, req party.ResponsePeerRequest) error {
	if !s.currentRaidAuthenticated(session) || req.Value != 0 {
		return s.sendRaidCommandFailure(session, op)
	}
	leader, bound := s.boundGameSessionCharacterSnapshot(session)
	s.raidMu.Lock()
	pending, found := s.currentRaidApplications[req.TargetID]
	state := s.raids[pending.raidKey]
	applicant := pending.applicant.session
	if !bound || !found || pending.leader != leader || !pending.expires.After(time.Now()) || state == nil || !state.CurrentLayout || state.LeaderID != leader.character || state.Started || state.CurrentUIFlags[0] != 0 || len(state.Members) >= currentRaidMemberMaximum(*state) || applicant == nil || applicant.channel.ID != session.channel.ID || applicant.channel.Type != session.channel.Type || s.runtimeRaidByMemberLocked(req.TargetID) != nil || !s.boundGameSessionCharacterCurrent(pending.applicant) || !s.boundGameSessionCharacterCurrent(pending.leader) || !s.currentRaidAuthenticated(applicant) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	row, available := s.currentRaidApplicantRecord(applicant, raid.CurrentOtherChannelJoinRequest{})
	if !available {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	member, placed := appendRuntimeRaidMemberWithLegalGroup(state, runtimeRaidMemberState{CharID: row.CharKey, Name: row.Name, Level: uint32(row.Level), Job: row.Job, GrowType: row.Grow, UserState: 1, HPPercent: 100, MPPercent: 100})
	if !placed {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	delete(s.currentRaidApplications, req.TargetID)
	snapshot := cloneRuntimeRaidState(*state)
	s.raidMu.Unlock()
	if err := s.sendGameUpperRawClassCodec(session, op, append([]byte{1}, party.BuildResponsePeerAckPayload(req.TargetID, 10)...), 1, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(applicant, snapshot, 2); err != nil {
		return err
	}
	if err := s.broadcastRuntimeRaidRefresh(snapshot); err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-raid-application-approved", "raid_key", snapshot.RaidKey, "applicant", req.TargetID, "leader", leader.character, "member_count", len(snapshot.Members), "group_index", member.GroupIndex, "slot_order", member.SlotOrder)
	return nil
}
