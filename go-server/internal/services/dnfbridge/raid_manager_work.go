package dnfbridge

import "longheng.io/server/internal/modules/dnf/raid"

// V212 客户端 C2S 0x29D (669) CmdPacketRaidManagerWork 的 ActionOrMode 语义：
//   - 0：移动成员到目标编队（TargetGroup 1..8）；当 TargetGroup == 0 时表示解散对应编队/移出编队回到待分配区（0x00C2DA72 / 0x00C2DB32 / 0x01DEFD32）。
//   - 1：委任团长（MemberCharKey = 新团长角色 ID，TargetGroup = 0；0x0353CA4F）。
const (
	raidManagerWorkActionMove           uint32 = 0
	raidManagerWorkActionDelegateLeader uint32 = 1
)

func (s *Service) dispatchOnlineRaidManagerWork(session *gameSession, typ uint16, body []byte) error {
	req, err := raid.DecodeManagerWorkRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-manager-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return nil
	}
	sourceID := selectedCharacterID(session)
	switch req.ActionOrMode {
	case raidManagerWorkActionMove:
		if req.TargetGroup == 0 {
			return s.handleOnlineRaidUnassignMember(session, typ, sourceID, uint16(req.MemberCharKey))
		}
		state, ok, projectionChanged := s.moveRuntimeRaidMember(sourceID, uint16(req.MemberCharKey), byte(req.TargetGroup))
		if !ok {
			s.logGameEvent(session, "game-raid-manager-rejected",
				"type", typ,
				"action", req.ActionOrMode,
				"source_char_id", sourceID,
				"member_char_id", req.MemberCharKey,
				"target_group", req.TargetGroup)
			return nil
		}
		if err := s.broadcastRuntimeRaidRoster(state); err != nil {
			return err
		}
		if projectionChanged {
			s.queueCurrentRaidRosterProgress(state)
		}
		return nil
	case raidManagerWorkActionDelegateLeader:
		return s.handleOnlineRaidDelegateLeader(session, typ, sourceID, uint16(req.MemberCharKey))
	default:
		s.logGameEvent(session, "game-raid-manager-action-unsupported",
			"type", typ,
			"action", req.ActionOrMode,
			"source_char_id", sourceID,
			"member_char_id", req.MemberCharKey,
			"target_group", req.TargetGroup)
		return nil
	}
}

func (s *Service) handleOnlineRaidUnassignMember(session *gameSession, typ uint16, sourceID, memberID uint16) error {
	state, ok, projectionChanged := s.unassignRuntimeRaidMember(sourceID, memberID)
	if !ok {
		s.logGameEvent(session, "game-raid-manager-unassign-rejected",
			"type", typ,
			"source_char_id", sourceID,
			"member_char_id", memberID)
		return nil
	}
	s.logGameEvent(session, "game-raid-manager-unassigned",
		"raid_key", state.RaidKey,
		"source_char_id", sourceID,
		"member_char_id", memberID)
	if err := s.broadcastRuntimeRaidRoster(state); err != nil {
		return err
	}
	if projectionChanged {
		s.queueCurrentRaidRosterProgress(state)
	}
	return nil
}

// unassignRuntimeRaidMember 将成员从所属编队移出回到待分配区（GroupIndex=0）。
// 客户端点击“解散对应编队”时会按成员逐个发送 ActionOrMode=0, TargetGroup=0。
func (s *Service) unassignRuntimeRaidMember(sourceID, memberID uint16) (runtimeRaidState, bool, bool) {
	if sourceID == 0 || memberID == 0 {
		return runtimeRaidState{}, false, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil || state.LeaderID != sourceID {
		return runtimeRaidState{}, false, false
	}
	index := runtimeRaidMemberIndex(*state, memberID)
	if index < 0 {
		return runtimeRaidState{}, false, false
	}
	if state.RouteOrRaidType == 2 && state.Started {
		return runtimeRaidState{}, false, false
	}
	groupChanged := state.Members[index].GroupIndex != 0
	state.Members[index].GroupIndex = 0
	state.Members[index].SlotOrder = 0
	state.Members[index].OrdinaryPartySolo = false
	if !state.Started && state.CurrentLayout && state.RouteOrRaidType <= 1 {
		state.Members[index].RecruitingPlacementExplicit = true
	}
	runtimeRaidSortMembers(state.Members)
	projectionChanged := false
	if s.options.currentClientVersion == "official-24.9" && groupChanged && state.Started && state.Battle != nil && !state.Battle.TimedOut {
		state.Battle.ProgressVersion++
		projectionChanged = true
	}
	return cloneRuntimeRaidState(*state), true, projectionChanged
}

func (s *Service) handleOnlineRaidDelegateLeader(session *gameSession, typ uint16, sourceID, targetID uint16) error {
	state, ok := s.delegateRuntimeRaidLeader(sourceID, targetID)
	if !ok {
		s.logGameEvent(session, "game-raid-leader-delegate-rejected",
			"type", typ,
			"source_char_id", sourceID,
			"target_char_id", targetID)
		return nil
	}
	s.logGameEvent(session, "game-raid-leader-delegated",
		"raid_key", state.RaidKey,
		"old_leader_id", sourceID,
		"new_leader_id", targetID)
	if err := s.broadcastRuntimeRaidRoster(state); err != nil {
		return err
	}
	if s.options.currentClientVersion == "official-24.9" {
		for _, member := range state.Members {
			peer, online := s.onlineGameSession(member.CharID)
			if !online {
				continue
			}
			if err := s.sendRuntimeRaidMetadata(peer, state, 2); err != nil {
				s.failRuntimeRaidRecipient(peer, "leader_delegate_metadata", err)
			}
		}
	}
	return nil
}

// delegateRuntimeRaidLeader 将攻坚队团长权限移交给同攻坚队内的在线成员。
func (s *Service) delegateRuntimeRaidLeader(sourceID, targetID uint16) (runtimeRaidState, bool) {
	if sourceID == 0 || targetID == 0 || sourceID == targetID {
		return runtimeRaidState{}, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil || state.LeaderID != sourceID {
		return runtimeRaidState{}, false
	}
	if runtimeRaidMemberIndex(*state, targetID) < 0 {
		return runtimeRaidState{}, false
	}
	if seat, reserved := s.currentRaidDisconnects[targetID]; state.CurrentLayout && reserved && seat.Raid == state {
		return runtimeRaidState{}, false
	}
	state.LeaderID = targetID
	return cloneRuntimeRaidState(*state), true
}
