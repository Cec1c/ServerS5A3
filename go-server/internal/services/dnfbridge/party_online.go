// party_online.go 处理只依赖当前 game 连接的临时多人组队回包。
// 这里不落 DB，也不替代真正 Party owner；它只把已在线且已选角的 session 组装成客户端可见的队伍快照。
package dnfbridge

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// All online-party mutations below are routed through RuntimePartyManager.
// This bridge keeps the current EXE's packet order and per-client projections
// while the manager remains the sole owner of party identity and membership.

const (
	entryIntoPartyFailureGeneric = 3
	entryIntoPartyFailureInvalid = 19
	entryIntoPartyFailureFull    = 20
	walkoutPartyFailureInvalid   = 19
	walkoutPartyFailureAuthority = 8
	// partyCommandFailureGeneric 是通用队伍命令失败码：op121（委任队长）等被拒时
	// 回 [0, 3]，与 705 entry-into-party 的通用失败分支同码，不再让客户端空等。
	partyCommandFailureGeneric = 3
	defaultPartyPeerPort       = 10000
)

func (s *Service) handleOnlinePartyCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	// The old connection can still deliver a buffered party command after a
	// reconnect has made a replacement session authoritative for the same
	// character. Never let that stale packet invite, join, leave, kick, or
	// rebuild the replacement's party. The current session directory is the
	// source of truth; a protocol response to the retired connection would
	// only corrupt its already-disposed client state, so consume it silently.
	if isOnlinePartyCommand(typ) && session != nil && session.selectedCharacterID != 0 {
		identity, current := s.boundGameSessionCharacterSnapshot(session)
		if !current || identity.session != session {
			s.logGameEvent(session, "game-party-command-stale-session-ignored",
				"type", typ,
				"char_id", session.selectedCharacterID,
				"character_generation", session.characterGeneration)
			return true, nil
		}
	}
	switch dnfenum.CmdPacket(typ) {
	case dnfenum.CmdPacketLeaveParty:
		// Always consume a leave request.  The client can send it after a peer
		// disconnect while its old singleton cache is already absent from the
		// central manager; returning it to the legacy path leaves the EXE's
		// non-leader gate intact.
		return true, s.handleOnlineLeaveParty(session, typ)
	case dnfenum.CmdPacketWalkoutPartyMember:
		return true, s.handleOnlineWalkoutPartyMember(session, typ, body)
	case dnfenum.CmdPacketChangeHost:
		return true, s.handleOnlineChangePartyHost(session, body)
	case dnfenum.CmdPacketChangePartyMemberPosition:
		return true, s.handleOnlineChangePartyMemberPosition(session, typ, body)
	case dnfenum.CmdPacketReserveLeaveParty:
		if s.hasManagedRuntimeParty(session) || hasMultiMemberRuntimeParty(session) {
			return true, s.handleOnlineReserveLeaveParty(session, typ, body)
		}
		return false, nil
	case dnfenum.CmdPacketRequestPeer:
		return true, s.handleOnlineRequestPeer(session, typ, body)
	case dnfenum.CmdPacketResponsePeer:
		return true, s.handleOnlineResponsePeer(session, typ, body)
	case dnfenum.CmdPacketEntryIntoParty:
		return true, s.handleOnlineEntryIntoParty(session, typ, body)
	case dnfenum.CmdPacketEntryIntoPartyFinish:
		return true, s.handleOnlineEntryIntoPartyFinish(session, typ)
	case dnfenum.CmdPacketEnterWarroom:
		return s.handleRuntimePartyDirectoryJoin(session, body)
	case dnfenum.CmdPacketQuickJoinRoom:
		return s.handleRuntimePartyDirectoryRefresh(session, body)
	default:
		return false, nil
	}
}

func isOnlinePartyCommand(typ uint16) bool {
	switch dnfenum.CmdPacket(typ) {
	case dnfenum.CmdPacketLeaveParty,
		dnfenum.CmdPacketWalkoutPartyMember,
		dnfenum.CmdPacketChangeHost,
		dnfenum.CmdPacketChangePartyMemberPosition,
		dnfenum.CmdPacketReserveLeaveParty,
		dnfenum.CmdPacketRequestPeer,
		dnfenum.CmdPacketResponsePeer,
		dnfenum.CmdPacketEntryIntoParty,
		dnfenum.CmdPacketEnterWarroom,
		dnfenum.CmdPacketQuickJoinRoom:
		return true
	default:
		return false
	}
}

func (s *Service) handleOnlineRequestPeer(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeRequestPeerRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-peer-request-body-invalid",
			"type", typ,
			"body_len", len(body),
			"error", err)
		return nil
	}
	sourceID := selectedCharacterID(session)
	// 频道隔离（彻底隔离口径）：目标在别的频道时**必须明确失败**——不回包会让客户端
	// "点了没反应"，静默丢弃更是无法复测；这里回通用失败包并记明 reason。
	// 只对"目标在线且频道不同"生效；目标离线/异区域仍走下面的既有静默路径。
	if targetSession, online := s.onlineGameSession(parsed.TargetID); online {
		if s.partyChannelMismatch(session, targetSession) {
			return s.answerPartyCommandRejection(session, typ, "request_peer", partyCommandFailureGeneric,
				"source_char_id", sourceID,
				"target_id", parsed.TargetID,
				"mode", parsed.Mode,
				"source_channel", currentTownPresenceChannelID(session),
				"target_channel", currentTownPresenceChannelID(targetSession),
				"reason", partyMemberChannelMismatchReason)
		}
	}
	if sourceID == 0 || parsed.TargetID == 0 || parsed.TargetID == sourceID ||
		s.onlinePlayers == nil || !s.onlinePlayers.PeerInSameArea(sourceID, parsed.TargetID) {
		// 这条**静默丢弃**是"邀请没反应"的另一半：被邀请方的客户端场景里没有邀请者的
		// actor（不同频道 / 不同城镇区域 / 被邀请方还在"每角色一份"的私有落点房里），
		// 原生 op7 处理器拿不到目标就画不出框。判据与代发路径
		// （party_force_invite.go 的 `invitee_outside_inviter_area`）同一条闸门，
		// 因此两个入口的现场日志可以并排 diff；把两边的分桶键一起打出来，
		// 一次复测就能判读是哪一种不同桶。
		sourceAreaText, sourceRegistered := s.townPresenceAreaText(sourceID)
		targetAreaText, targetRegistered := s.townPresenceAreaText(parsed.TargetID)
		s.logGameEvent(session, "game-peer-request-target-outside-area",
			"type", typ,
			"source_char_id", sourceID,
			"target_id", parsed.TargetID,
			"mode", parsed.Mode,
			"source_area", sourceAreaText,
			"source_registered", sourceRegistered,
			"target_area", targetAreaText,
			"target_registered", targetRegistered,
			"reason", "peer_in_same_area_gate_is_the_shared_prerequisite_of_the_client_invite_path_and_the_force_trigger")
		return nil
	}
	s.logGameEvent(session, "game-peer-request-accepted",
		"type", typ,
		"source_char_id", sourceID,
		"target_id", parsed.TargetID,
		"mode", parsed.Mode,
		"body_source", "current_exe_sub_325F9D0_request_sub_2261E30_ack_and_sub_1D82CF0_mode15_selection_notice")
	if err := s.sendGameUpperRawClassCodec(
		session,
		typ,
		party.BuildRequestPeerAck(),
		dnfproto.DefaultChannelClassification,
		true,
	); err != nil {
		return err
	}
	if parsed.Mode == 15 {
		// The town interaction menu reads the target actor's current party link,
		// not an invite/join flag from the mode-15 selection notice. Refresh that
		// link at click time from the target's authoritative runtime state so a
		// partyless target offers invite while an active target offers join. This
		// also repairs a stale client-side link left by an earlier party lifecycle.
		targetSession, online := s.onlineGameSession(parsed.TargetID)
		if !online {
			s.logGameEvent(session, "game-peer-selection-party-state-deferred",
				"source_char_id", sourceID,
				"target_id", parsed.TargetID,
				"reason", "target_resident_session_unavailable")
			return nil
		}
		targetParty := runtimePartyStateSnapshot(targetSession)
		if s.options.currentClientVersion == "official-24.9" {
			targetParty = s.runtimePartyStateForSession(targetSession)
		}
		projected, projectionErr := s.sendCurrentPartyActorFrameProjection(
			session,
			targetSession,
			targetParty,
			"peer_selection_dynamic_target_party_state",
		)
		if projectionErr != nil {
			return projectionErr
		}
		if !projected {
			s.logGameEvent(session, "game-peer-selection-party-state-deferred",
				"source_char_id", sourceID,
				"target_id", parsed.TargetID,
				"target_party_id", targetParty.PartyID,
				"reason", "target_actor_projection_unavailable")
			return nil
		}
		targetPartyActive := targetParty.PartyID > 0 && len(runtimePartyMembers(targetParty)) > 0
		selectionBody := party.BuildRequestPeerSelectionNotice(parsed.TargetID, parsed.Value0, targetPartyActive)
		s.logGameEvent(session, "game-peer-selection-notice-send",
			"type", 0x0007,
			"source_char_id", sourceID,
			"target_id", parsed.TargetID,
			"target_party_id", targetParty.PartyID,
			"target_party_active", targetPartyActive,
			"party_marker", func() string {
				if targetPartyActive {
					return "0000"
				}
				return "ffff"
			}(),
			"mode", parsed.Mode,
			"body_len", len(selectionBody),
			"body_source", "current_exe_sub_1D82CF0_mode15_party_marker_ffff_absent_else_present_then_sub_325FA70_opens_menu")
		return s.sendGameUpperRawClassCodec(session, 0x0007, selectionBody, 0, true)
	}

	if !isForwardedPeerRequestMode(parsed.Mode) {
		return nil
	}
	sourceIdentity, sourceBound := s.boundGameSessionCharacterSnapshot(session)
	targetIdentity, targetBound := s.onlineGameSessionCharacterSnapshot(parsed.TargetID)
	if !sourceBound || !targetBound || targetIdentity.session == nil {
		return nil
	}
	inviteRecorded := false
	var recordedRequest party.RuntimePeerRequest
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if s.options.currentClientVersion == "official-24.9" {
			recordedRequest, inviteRecorded = manager.RecordOnlinePeerRequest(
				targetIdentity.character, targetIdentity.generation,
				sourceIdentity.character, sourceIdentity.generation, parsed.Mode,
			)
		} else {
			var partyID uint16
			if snapshot, found := manager.SnapshotByUser(sourceIdentity.character, sourceIdentity.generation); found {
				partyID = snapshot.ID
			}
			inviteRecorded = manager.RecordInvite(targetIdentity.character, targetIdentity.generation,
				sourceIdentity.character, sourceIdentity.generation, partyID, parsed.Mode)
			recordedRequest = party.RuntimePeerRequest{PartyID: partyID, JoinerID: targetIdentity.character, AnchorID: sourceIdentity.character}
		}
	}
	if !inviteRecorded {
		s.logGameEvent(session, "game-peer-request-invite-deferred",
			"type", typ,
			"source_char_id", sourceID,
			"target_id", parsed.TargetID,
			"mode", parsed.Mode,
			"reason", "session_generation_changed_before_invite_registration")
		return nil
	}
	s.logGameEvent(session, "game-party-peer-request-direction-registered",
		"char_id", sourceID, "target_char_id", parsed.TargetID, "mode", parsed.Mode,
		"party_id", recordedRequest.PartyID, "joiner_char_id", recordedRequest.JoinerID,
		"leader_char_id", recordedRequest.AnchorID)
	noticeBody := party.BuildRequestPeerNotice(sourceID, parsed)
	s.logGameEvent(session, "game-peer-request-notice-forward",
		"type", 0x0007,
		"source_char_id", sourceID,
		"target_id", parsed.TargetID,
		"mode", parsed.Mode,
		"body_len", len(noticeBody),
		"body_source", "current_exe_sub_1D82CF0_mode_specific_peer_prompt")
	return s.sendGameUpperRawClassCodec(targetIdentity.session, 0x0007, noticeBody, 0, true)
}

func (s *Service) handleOnlineLeaveParty(session *gameSession, typ uint16) error {
	sourceID := selectedCharacterID(session)
	state := runtimePartyStateSnapshot(session)
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if sourceID == 0 || !bound {
		if s.partyLeaveRepair() == partyLeaveRepairLegacy {
			// 一键回退档：逐字节回到改动前——这种情况一个字节都不回、也不记事件。
			return nil
		}
		// 一个字节都不回会让客户端既不回滚也不提示（"点了没反应"）。这里的会话没有可动的
		// 队伍状态（没选角），或者已经不是这个角色的当前权威会话，所以只记事件 + 回失败包。
		s.logGameEvent(session, "game-party-leave-unbound-session",
			"type", typ,
			"source_char_id", sourceID,
			"character_generation", session.characterGeneration,
			"reason", "session_has_no_current_authoritative_character")
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	nextState, result, removed := s.leaveManagedRuntimeParty(identity, "client_leave_party_op13")
	if !removed && s.partyLeaveRepair() == partyLeaveRepairOn {
		// 缺陷（操作者："p2 点击的是退出队伍，但是仍然在队伍里面"）：
		// `removed=false` 有两种完全不同的成因，改动前被当成同一种处理。
		// 管理器里**仍然有**这个角色、只是它记的 SessionGeneration 与当前权威会话不一致时
		// （重连/重选角/同角色双 socket 之后），只清自己的客户端投影、只回 ACK，
		// 会让队长永远看不到 p2 离队。这里按**角色**（不按代际）再摘一次。
		if repairedState, repairedResult, repaired := s.repairManagedRuntimePartyLeave(identity); repaired {
			s.logGameEvent(session, "game-party-leave-repaired",
				"type", typ,
				"source_char_id", sourceID,
				"reason", result.Reason,
				"session_generation", identity.generation,
				"party_id", repairedState.PartyID,
				"leader_char_id", repairedState.UserID,
				"member_count", len(runtimePartyMembers(repairedState)),
				"wire_contract", "leave_by_character_after_manager_generation_drift_then_broadcast_to_survivors")
			nextState, result, removed = repairedState, repairedResult, true
		}
	}
	if !removed {
		// 走到这里只剩两种可能：①（repair 档）管理器里确实已经没有这个角色了，
		// 客户端还留着一份投影残留；②`legacy` 回退档下代际漂移的情形。
		// ①正是本分支的原始用途：Do not let an unimportable one-member cache survive
		// just because the manager has already removed it.  A successful native leave
		// ACK plus an empty roster is idempotent and restores solo dungeon permission.
		s.clearRuntimePartyProjection(identity.character, identity.generation)
		s.logGameEvent(session, "game-party-leave-stale-projection-cleared",
			"type", typ,
			"source_char_id", sourceID,
			"reason", result.Reason)
		if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
			return err
		}
		return s.sendRuntimePartyMemberRemoved(session, sourceID, alignedcmd.PartyState{})
	}
	if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if err := s.sendRuntimePartyMemberRemoved(session, sourceID, alignedcmd.PartyState{}); err != nil {
		return err
	}
	if result.Retired != nil {
		s.closeRuntimePartyUDPRelay(int(result.Retired.ID))
		for _, member := range result.Retired.Members {
			if member.UserID == sourceID {
				continue
			}
			if targetSession, online := s.onlineGameSession(member.UserID); online {
				if err := s.sendRuntimePartySnapshot(targetSession, alignedcmd.PartyState{}); err != nil {
					return err
				}
			}
		}
		return s.sendManagedRuntimePartySnapshots(nextState)
	}
	if nextState.PartyID > 0 {
		s.syncRuntimePartyUDPRelay(nextState)
	} else {
		s.closeRuntimePartyUDPRelay(state.PartyID)
	}
	if err := s.broadcastRuntimePartyMemberRemoved(state, nextState, sourceID, session); err != nil {
		return err
	}
	return nil
}

// repairManagedRuntimePartyLeave 是"管理器仍持有该角色、但记录的会话代际已经漂移"时的
// 按角色无条件离队。除 `LeaveCurrent`（不校验代际）之外与 `leaveManagedRuntimeParty`
// 逐字同构，因此成功路径的广播/中继/投影行为与正常离队完全一致。
//
// 前置条件与 `leaveManagedRuntimeParty` 相同并且**再次复查**：请求必须来自该角色当前的
// 权威会话（`boundGameSessionCharacterCurrent`）。退役 socket 到不了这里
// （`handleOnlinePartyCommand` 已经把它整条消费掉）。
func (s *Service) repairManagedRuntimePartyLeave(identity boundGameSessionCharacter) (alignedcmd.PartyState, party.RuntimePartyResult, bool) {
	if !s.boundGameSessionCharacterCurrent(identity) {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "stale_session"}, false
	}
	manager := s.runtimePartyManagerForService()
	if manager == nil {
		return alignedcmd.PartyState{}, party.RuntimePartyResult{Reason: "party_manager_unavailable"}, false
	}
	before, _ := manager.SnapshotByUser(identity.character, 0)
	result := manager.LeaveCurrent(identity.character)
	if !result.OK {
		return alignedcmd.PartyState{}, result, false
	}
	s.logRuntimePartyMembershipRemoved("leave_repair_by_character", identity.character, identity.generation, before, result)
	s.clearRuntimePartyProjection(identity.character, identity.generation)
	if result.Disbanded || result.Party.ID == 0 {
		return alignedcmd.PartyState{}, result, true
	}
	return s.publishRuntimePartyMembershipSnapshot(result.Party), result, true
}

func (s *Service) handleOnlineWalkoutPartyMember(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeWalkoutPartyMemberRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-party-walkout-body-short", "type", typ, "body_len", len(body), "error", err)
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(identity.character, identity.generation)
	if !found {
		snapshot, found = s.bootstrapManagedRuntimePartyForSession(session)
	}
	if !found {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	if snapshot.Leader != identity.character {
		s.logGameEvent(session, "game-party-walkout-not-leader",
			"type", typ,
			"source_char_id", identity.character,
			"leader_id", snapshot.Leader,
			"slot", parsed.Slot)
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureAuthority)
	}
	var target party.RuntimePartyMember
	for _, member := range snapshot.Members {
		if member.Slot == parsed.Slot {
			target = member
			break
		}
	}
	if target.UserID == 0 || target.UserID == identity.character {
		s.logGameEvent(session, "game-party-walkout-invalid-slot",
			"type", typ,
			"source_char_id", identity.character,
			"slot", parsed.Slot,
			"member_count", len(snapshot.Members))
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	previousState := snapshot.StateFor(identity.character)
	kickedSnapshot := snapshot
	result := manager.Kick(identity.character, identity.generation, target.UserID, target.SessionGeneration)
	if !result.OK {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	s.logRuntimePartyMembershipRemoved("leader_kick_walkout", target.UserID, target.SessionGeneration, kickedSnapshot, result)
	nextState := s.publishRuntimePartyMembershipSnapshot(result.Party)
	if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if err := s.broadcastRuntimePartyWalkoutNotice(previousState, parsed.Slot, 0); err != nil {
		return err
	}
	if nextState.PartyID <= 0 {
		s.closeRuntimePartyUDPRelay(int(result.Party.ID))
	} else {
		s.syncRuntimePartyUDPRelay(nextState)
	}
	if targetSession, ok := s.onlineGameSession(target.UserID); ok {
		s.clearRuntimePartyProjection(target.UserID, target.SessionGeneration)
		if err := s.sendRuntimePartyMemberRemoved(targetSession, target.UserID, alignedcmd.PartyState{}); err != nil {
			return err
		}
	}
	if err := s.broadcastRuntimePartyMemberRemoved(previousState, nextState, target.UserID, nil); err != nil {
		return err
	}
	return nil
}

// handleOnlineChangePartyHost implements the current client's real delegate-
// leader command. NoPack sends class1/op121 with one selected party slot. The
// party window derives leadership from slot zero, so changing only UserID is
// insufficient: clear the old client-side table, move the new leader to slot
// zero, then rebuild op9 for every member. The already-connected P2P transport
// must remain intact: replaying op153/op11 here makes the current EXE start a
// second endpoint handshake and leaves one side stuck in "connecting".
// answerPartyCommandRejection 记录拒绝并把 [0, code] 失败包回给客户端。
//
// 这些拒绝路径此前只写日志、一个包都不回，玩家看到的就是"点了没反应"。
// 线上证据（2026-09-24 双开实测）：被邀请方点了"委任队长"，客户端发出 op121
// CHANGE_HOST，服务端因 sender_is_not_leader 静默丢弃 —— 客户端既不回滚也不提示，
// 表现就是"委任队长功能没有反应"。回答失败包至少让客户端拿到结果。
func (s *Service) answerPartyCommandRejection(session *gameSession, typ uint16, operation string, code byte, fields ...any) error {
	args := make([]any, 0, len(fields)+4)
	args = append(args, "operation", operation, "error_code", code)
	args = append(args, fields...)
	s.logGameEvent(session, "game-party-command-rejected", args...)
	return s.sendPartyCommandFailure(session, typ, code)
}

func (s *Service) handleOnlineChangePartyHost(session *gameSession, body []byte) error {
	const changeHostOpcode = uint16(dnfenum.CmdPacketChangeHost)
	if len(body) < 1 {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", selectedCharacterID(session), "body_len", len(body), "reason", "empty_body")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", selectedCharacterID(session), "body_len", len(body), "reason", "stale_session")
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(identity.character, identity.generation)
	if !found {
		snapshot, found = s.bootstrapManagedRuntimePartyForSession(session)
	}
	if !found {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", identity.character, "selected_slot", body[0], "reason", "no_runtime_party")
	}
	// The client emits the same op121 twice for one click. If the live party has
	// already published this sender as a non-leader under the requested party,
	// the second packet is the repeat of an already committed transfer. Answering
	// it with a failure or repainting the roster makes the just-updated window
	// briefly show an error or refresh again.
	if snapshot.Leader != identity.character && identity.session != nil &&
		identity.session.lastPublishedPartyLeader == snapshot.Leader &&
		identity.session.lastPublishedPartyID == int(snapshot.ID) &&
		changeHostSlotSelectsLeader(snapshot, body[0]) {
		return s.republishChangeHostIdempotent(session, snapshot, body[0], "sender_already_delegated_by_the_first_request")
	}
	if snapshot.Leader != identity.character {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", identity.character, "party_id", snapshot.ID, "selected_slot", body[0],
			"leader_char_id", snapshot.Leader, "reason", "sender_is_not_leader")
	}
	// 客户端给的字节是队伍框里的槽位。先按前端的 slot 精确匹配；匹配不到时
	// 再按"槽位顺序的下标"回退一次（0 基/1 基各试一次），避免因为槽位编号
	// 与显示序号的差异导致移交被静默丢弃。
	target, matched := runtimePartySnapshotMemberBySlot(snapshot, body[0])
	if !matched {
		if ordered := runtimePartyMembersBySlot(snapshot); len(ordered) > 0 {
			switch {
			case int(body[0]) < len(ordered):
				target, matched = ordered[body[0]], true
			case body[0] >= 1 && int(body[0]-1) < len(ordered):
				target, matched = ordered[body[0]-1], true
			}
		}
	}
	// 幂等（第二形态）：发出方**仍然是权威队长**，而这条请求解析出的目标就是队长本人
	// （客户端界面上那一行还是它自己）。权威名单已经正确，重复请求不发窗口刷新。
	if target.UserID == snapshot.Leader || target.UserID == identity.character {
		if err := s.republishChangeHostIdempotent(session, snapshot, body[0], "sender_is_already_the_authoritative_leader"); err != nil {
			return err
		}
		return nil
	}
	if !matched || target.UserID == 0 {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", identity.character, "party_id", snapshot.ID, "selected_slot", body[0],
			"available_slots", runtimePartySlotList(snapshot), "reason", "target_slot_not_found")
	}
	result := manager.TransferLeader(identity.character, identity.generation, target.UserID, target.SessionGeneration)
	if !result.OK || result.Previous == nil {
		return s.answerPartyCommandRejection(session, changeHostOpcode, "change_host", partyCommandFailureGeneric,
			"char_id", identity.character, "party_id", snapshot.ID, "selected_slot", body[0],
			"target_char_id", target.UserID, "reason", "manager_transfer_failed")
	}

	// Host transfer keeps the party ID and all peer endpoints. No relay close,
	// empty roster, actor removal or formation handshake is needed.
	state := s.publishRuntimePartySnapshot(result.Party)
	s.syncRuntimePartyUDPRelay(state)
	if err := s.sendChangeHostRuntimePartySnapshots(state); err != nil {
		return err
	}
	// Mark the former leader only after the committed projection was delivered.
	// A write failure therefore remains retryable instead of being mistaken for a
	// completed duplicate on the next op121.
	if identity.session != nil {
		identity.session.lastPublishedPartyLeader = target.UserID
		identity.session.lastPublishedPartyID = int(result.Party.ID)
	}
	s.logGameEvent(session, "game-party-change-host-committed",
		"source_char_id", identity.character,
		"new_leader_id", target.UserID,
		"selected_slot", body[0],
		"old_party_id", result.Previous.ID,
		"new_party_id", result.Party.ID,
		"member_count", len(result.Party.Members),
		"wire_contract", "atomic_host_projection_without_clear_window_or_endpoint_rehandshake")
	return nil
}

func changeHostSlotSelectsLeader(snapshot party.RuntimePartySnapshot, slot byte) bool {
	member, found := runtimePartySnapshotMemberBySlot(snapshot, slot)
	return found && member.UserID == snapshot.Leader
}

// republishChangeHostIdempotent 处理"同一条委任队长请求的重发"：
// **不退役代数、不 bootstrap 新队伍，也不再重发窗口帧**。第一条请求已经提交并
// 投影过同一份权威名单；再画一遍只会让队伍窗口在同一状态上刷新一次。
// 重发仍留下可判读事件，便于现场区分客户端重发和服务端重复变更。
func (s *Service) republishChangeHostIdempotent(
	session *gameSession,
	snapshot party.RuntimePartySnapshot,
	selectedSlot byte,
	reason string,
) error {
	s.logGameEvent(session, "game-party-change-host-idempotent",
		"source_char_id", selectedCharacterID(session),
		"leader_char_id", snapshot.Leader,
		"selected_slot", selectedSlot,
		"party_id", snapshot.ID,
		"member_count", len(snapshot.Members),
		"reason", reason,
		"response_frames", 0,
		"wire_contract", "client_repeat_of_an_already_committed_host_change_is_a_silent_noop")
	return nil
}

func (s *Service) handleOnlineChangePartyMemberPosition(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeChangePartyMemberPositionRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-party-change-position-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(identity.character, identity.generation)
	if !found {
		snapshot, found = s.bootstrapManagedRuntimePartyForSession(session)
	}
	if !found || len(snapshot.Members) <= 1 {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	if snapshot.Leader != identity.character {
		s.logGameEvent(session, "game-party-change-position-not-leader",
			"type", typ,
			"source_char_id", identity.character,
			"leader_id", snapshot.Leader,
			"slot", parsed.Slot,
			"position", parsed.Position)
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureAuthority)
	}
	visibleState := snapshot.StateFor(identity.character)
	fromMember, sourceOK := runtimePartyMemberBySlot(visibleState, parsed.Slot)
	if !sourceOK || fromMember.UserID == 0 || fromMember.UserID == snapshot.Leader {
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	fromSlot := fromMember.Slot
	toSlot := parsed.Position
	if destination, destinationOK := runtimePartyMemberBySlot(visibleState, parsed.Position); destinationOK && destination.UserID != 0 {
		toSlot = destination.Slot
	}
	result := manager.Reposition(identity.character, identity.generation, fromSlot, toSlot)
	if !result.OK {
		s.logGameEvent(session, "game-party-change-position-noop",
			"type", typ,
			"source_char_id", identity.character,
			"slot", parsed.Slot,
			"position", parsed.Position,
			"member_count", len(snapshot.Members))
		return s.sendPartyCommandFailure(session, typ, walkoutPartyFailureInvalid)
	}
	nextState := s.publishRuntimePartySnapshot(result.Party)
	if err := s.broadcastRuntimePartyChangePosition(nextState, parsed.Slot, parsed.Position); err != nil {
		return err
	}
	return nil
}

func (s *Service) broadcastRuntimePartyWalkoutNotice(state alignedcmd.PartyState, slot byte, mode byte) error {
	body := party.BuildWalkoutPartyMemberNotice(slot, mode)
	for _, member := range runtimePartyMembers(state) {
		session, ok := s.onlineGameSession(member.UserID)
		if !ok {
			continue
		}
		if err := s.sendGameUpperRawClassCodec(session, uint16(dnfenum.CmdPacketRequestPeer), body, dnfproto.DefaultChannelClassification, true); err != nil {
			return err
		}
	}
	return nil
}

// sendDungeonPartyFrameRoster 在**副本上下文**里把队伍帧重新发给同一场副本里的成员：
// op153（血条/槽位实时表）+ 每个成员一条 op9 队伍记录（自己的 + 队友的）。
//
// 城镇那条路是 `broadcastCurrentPartyActorProjection` → `onlinePlayers.PeersInSameArea`（城镇
// 区域在线表），而进副本时 `leaveTownPresenceForDungeon` 已经把双方从城镇区域表摘掉 ⇒ 副本内
// op9 只能靠进图落位那一次投影（`projectDungeonPartyPeerActors`，每个方向只发一次）。
// 队伍关系在副本里发生变化时（委任队长/调整位置/成员进出）必须再补一轮，否则客户端队伍框
// 里仍然是变化前那份槽位+血条数据：现场（2026-09-25 双开）症状就是"转让队长后血条 UI 出错、
// 两个队员的框互相交换"，随后进图双方都看不到对方。
//
// 判据只用 `currentDungeonPartyRunKey`（各自的 dungeonPartyRunMu，不碰任何会话的 dungeon.mu），
// 并且要求两人落在**同一个副本运行键**上，避免把不同场的成员画到彼此房间里。
func (s *Service) sendDungeonPartyFrameRoster(state alignedcmd.PartyState, source string) int {
	if s == nil || state.PartyID <= 0 {
		return 0
	}
	members := runtimePartyMembers(state)
	if len(members) < 2 {
		return 0
	}
	sessions := make([]*gameSession, 0, len(members))
	var runKey dungeonPartyRunKey
	clearedStaleKeys := 0
	for _, member := range members {
		session, online := s.onlineGameSession(member.UserID)
		if !online || session == nil {
			continue
		}
		// 城镇上下文的收口点：本函数也会在**城镇**里被
		// `sendManagedRuntimePartySnapshots`（party_manager_runtime.go:593）调用，
		// 所以这里读键前必须先清掉"队伍已经解散/已经回城"留下的过期副本运行键，
		// 否则新队伍会在城镇里收到 owner=0 的副本上下文队伍帧（现场异常见
		// dungeon_party_run_key_town_reset.go 的文件头）。
		if s.forgetStaleDungeonPartyRunKeyOutsideDungeon(session, source) {
			clearedStaleKeys++
		}
		key, ready := s.currentDungeonPartyRunKey(session)
		if !ready {
			continue
		}
		if len(sessions) == 0 {
			runKey = key
		} else if key != runKey {
			// 两人不在同一场副本里：只发落在第一场那批，不发跨场的 actor 记录。
			continue
		}
		sessions = append(sessions, session)
	}
	if len(sessions) < 2 {
		// 修复档下"清掉了过期键 ⇒ 这次不可能是副本上下文队伍帧"要留一条判读行，
		// 否则线上只能看到"这一天没有 roster 事件"，判不出是被修掉还是别的原因。
		if clearedStaleKeys > 0 {
			s.logGameEvent(nil, "game-dungeon-party-frame-roster-skipped",
				"source", source,
				"party_id", state.PartyID,
				"member_count", len(members),
				"dungeon_member_count", len(sessions),
				"cleared_stale_run_keys", clearedStaleKeys,
				"reason", "no_member_has_a_valid_dungeon_run_key_after_the_town_reset",
				"switch", dungeonPartyRunKeyTownResetEnv,
				"mode", dungeonPartyRunKeyTownResetMode())
		}
		return 0
	}
	delivered := 0
	for _, receiver := range sessions {
		if err := s.sendGameUpperRawClassCodec(receiver, 0x0099, runtimePartyRealtimeInfoBody(state), 0, true); err != nil {
			s.logGameEvent(receiver, "game-dungeon-party-frame-roster-failed",
				"conn_id", receiver.connID,
				"source", source,
				"stage", "op153_realtime_info",
				"party_id", state.PartyID,
				"error", err)
			continue
		}
		for _, actor := range sessions {
			sent, err := s.sendCurrentPartyActorFrameProjectionForDungeonPeer(
				receiver,
				actor,
				state,
				source+"_dungeon_roster",
			)
			if err != nil {
				s.logGameEvent(receiver, "game-dungeon-party-frame-roster-failed",
					"conn_id", receiver.connID,
					"source", source,
					"stage", "op9_party_record",
					"party_id", state.PartyID,
					"actor_char_id", actor.selectedCharacterID,
					"error", err)
				continue
			}
			if sent {
				delivered++
			}
		}
	}
	s.logGameEvent(sessions[0], "game-dungeon-party-frame-roster-send",
		"source", source,
		"party_id", state.PartyID,
		"member_count", len(members),
		"dungeon_member_count", len(sessions),
		"record_count", delivered,
		"run_key_room", fmt.Sprintf("%d:%d", runKey.RoomX, runKey.RoomY),
		"run_key_dungeon_id", runKey.DungeonID,
		"run_key_maze_index", runKey.MazeIndex,
		"sequence", "op153_then_op9_per_member_for_each_receiver")
	return delivered
}

func (s *Service) broadcastRuntimePartyChangePosition(state alignedcmd.PartyState, fromSlot byte, toSlot byte) error {
	body := party.BuildChangePartyMemberPositionAck(toSlot, 1)
	for _, member := range runtimePartyMembers(state) {
		session, ok := s.onlineGameSession(member.UserID)
		if !ok {
			continue
		}
		if err := s.sendGameUpperRawClassCodec(session, uint16(dnfenum.CmdPacketChangePartyMemberPosition), body, dnfproto.DefaultChannelClassification, true); err != nil {
			return err
		}
		if err := s.sendRuntimePartyRosterLocal(session, state, "change_party_member_position_after_ack"); err != nil {
			return err
		}
	}
	// 城镇路径已经给每个成员重发过自己的名册；副本里没有城镇区域表，必须补一轮双人队伍帧。
	s.sendDungeonPartyFrameRoster(state, "change_party_member_position")
	return nil
}

func (s *Service) handleOnlineReserveLeaveParty(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeReserveLeavePartyRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-party-reserve-leave-body-short", "type", typ, "body_len", len(body), "error", err)
		return s.sendGameUpperRawClassCodec(session, typ, []byte{0}, dnfproto.DefaultChannelClassification, true)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperRawClassCodec(session, typ, []byte{0}, dnfproto.DefaultChannelClassification, true)
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(identity.character, identity.generation)
	if !found {
		snapshot, found = s.bootstrapManagedRuntimePartyForSession(session)
	}
	if !found {
		return s.sendGameUpperRawClassCodec(session, typ, []byte{0}, dnfproto.DefaultChannelClassification, true)
	}
	bodyResp := party.BuildReserveLeavePartyAck(parsed.Flag, identity.character)
	for _, member := range snapshot.Members {
		targetSession, ok := s.onlineGameSession(member.UserID)
		if !ok {
			continue
		}
		if err := s.sendGameUpperRawClassCodec(targetSession, typ, bodyResp, dnfproto.DefaultChannelClassification, true); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-party-reserve-leave-broadcast",
		"type", typ,
		"source_char_id", identity.character,
		"flag", parsed.Flag,
		"member_count", len(snapshot.Members))
	return nil
}

func (s *Service) handleOnlineResponsePeer(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeResponsePeerRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-party-response-peer-body-short", "type", typ, "body_len", len(body), "error", err)
		return nil
	}
	s.logGameEvent(session, "game-party-response-peer-received",
		"type", typ,
		"source_char_id", selectedCharacterID(session),
		"target_id", parsed.TargetID,
		"mode", parsed.Mode,
		"value", parsed.Value)
	if !isForwardedPeerRequestMode(parsed.Mode) {
		return nil
	}
	acceptorIdentity, acceptorBound := s.boundGameSessionCharacterSnapshot(session)
	targetIdentity, targetBound := s.onlineGameSessionCharacterSnapshot(parsed.TargetID)
	// 频道隔离（彻底隔离口径）：接受跨频道邀请同样必须明确失败，并且**不消费**邀请记录
	// （消费掉会让邀请方看到"对方已处理"的假象）。
	if targetBound && targetIdentity.session != nil && s.partyChannelMismatch(session, targetIdentity.session) {
		return s.answerPartyCommandRejection(session, typ, "response_peer", partyCommandFailureGeneric,
			"source_char_id", selectedCharacterID(session),
			"target_id", parsed.TargetID,
			"mode", parsed.Mode,
			"source_channel", currentTownPresenceChannelID(session),
			"target_channel", currentTownPresenceChannelID(targetIdentity.session),
			"reason", partyMemberChannelMismatchReason)
	}
	var recordedRequest party.RuntimePeerRequest
	inviteConsumed := false
	if acceptorBound && targetBound {
		if manager := s.runtimePartyManagerForService(); manager != nil {
			recordedRequest, inviteConsumed = manager.ConsumePeerRequest(
				acceptorIdentity.character,
				acceptorIdentity.generation,
				targetIdentity.character,
				targetIdentity.generation,
				parsed.Mode,
			)
		}
	}
	if !inviteConsumed {
		s.logGameEvent(session, "game-peer-response-without-pending-request",
			"type", typ,
			"source_char_id", selectedCharacterID(session),
			"target_id", parsed.TargetID,
			"mode", parsed.Mode)
		return nil
	}

	// Direction was fixed before the recipient opened its local op12 window.
	// A changed/retired target party must not silently turn into a new lobby.
	if isPartyInviteResponse(parsed) && !peerResponseRejected(parsed) && recordedRequest.PartyID != 0 {
		snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(recordedRequest.AnchorID, 0)
		if !found || snapshot.ID != recordedRequest.PartyID || snapshot.Leader != recordedRequest.AnchorID {
			s.logGameEvent(session, "game-party-peer-request-target-changed",
				"char_id", selectedCharacterID(session), "party_id", recordedRequest.PartyID,
				"leader_char_id", recordedRequest.AnchorID)
			return nil
		}
	}

	ackPayload := party.BuildResponsePeerAckPayload(parsed.TargetID, parsed.Mode)
	if err := s.sendGameUpperSuccess(session, typ, ackPayload); err != nil {
		return err
	}
	targetSession := targetIdentity.session
	if targetSession == nil || session == nil || session.selectedCharacterID == 0 {
		s.logGameEvent(session, "game-party-response-peer-target-offline",
			"type", typ,
			"source_char_id", selectedCharacterID(session),
			"target_id", parsed.TargetID)
		return nil
	}
	responseNotice := party.BuildResponsePeerNotice(session.selectedCharacterID, parsed.Mode, parsed.Value)
	if err := s.sendGameUpperRawClassCodec(targetSession, 0x0008, responseNotice, 0, true); err != nil {
		return err
	}
	if peerResponseRejected(parsed) {
		s.logGameEvent(session, "game-peer-response-rejected",
			"type", typ,
			"source_char_id", selectedCharacterID(session),
			"target_id", parsed.TargetID,
			"mode", parsed.Mode,
			"value", parsed.Value)
		return nil
	}
	if parsed.Mode == 1 {
		s.beginOnlineItemTrade(session, targetSession)
		s.logGameEvent(session, "game-trade-response-peer-forwarded",
			"type", typ,
			"source_char_id", session.selectedCharacterID,
			"target_id", parsed.TargetID,
			"mode", parsed.Mode,
			"value", parsed.Value,
			"body_source", "current_exe_class1_op11_trade_ack_and_class0_op8_peer_notice")
		return nil
	}
	if !isPartyInviteResponse(parsed) {
		return nil
	}
	joiner, anchor := session, targetSession
	if recordedRequest.JoinerID == targetIdentity.character && recordedRequest.AnchorID == acceptorIdentity.character {
		joiner, anchor = targetSession, session
	}
	if invitePartyID := runtimePartyStateSnapshot(joiner).PartyID; invitePartyID > 0 {
		if targetPartyID := runtimePartyStateSnapshot(anchor).PartyID; invitePartyID != targetPartyID {
			if err := s.sendRuntimePartySnapshot(joiner, alignedcmd.PartyState{}); err != nil {
				return err
			}
			s.logGameEvent(joiner, "game-party-invitee-stale-projection-cleared",
				"type", typ,
				"char_id", joiner.selectedCharacterID,
				"target_id", selectedCharacterID(anchor),
				"stale_party_id", invitePartyID,
				"target_party_id", targetPartyID)
		}
	}
	state, result, ok := s.createOrJoinManagedRuntimeParty(joiner, anchor)
	if !ok {
		s.logGameEvent(session, "game-party-response-peer-party-full",
			"type", typ,
			"source_char_id", session.selectedCharacterID,
			"target_id", parsed.TargetID,
			"reason", result.Reason)
		return nil
	}
	if err := s.sendManagedRuntimePartySnapshots(state); err != nil {
		return err
	}
	// 加入成功后再无条件给**被邀请方**补一次权威名单（op153+op9，不带 op11 握手）。
	//
	// 现场（2026-09-24 21:47，角色 3 邀请角色 2）：服务端判定完全正确
	// （game-dungeon-select-party-context char_id=2 … is_leader=false），但被邀请方的
	// 客户端界面上自己仍是队长。上面的 stale 清理只在"服务端视角里它已有非零 party id"
	// 时才触发，而这次没有触发（整段日志没有 game-party-invitee-stale-projection-cleared）
	// ——它那份"我是队长"的显示来自本地 op12 投影，服务端无从感知，所以清理条件不能作为
	// 唯一保障：加入之后必须无条件再画一次权威名单。
	//
	// 用 roster 路径（op153+op9）而不是整份 snapshot：op11 是 UDP 对端握手，
	// 刚建立对端后再重放会让当前客户端拆掉健康连接进入 connecting（仓库既有注释）。
	if manager := s.runtimePartyManagerForService(); manager != nil {
		if snapshot, found := manager.SnapshotByID(uint16(state.PartyID)); found {
			joinerState := snapshot.StateFor(joiner.selectedCharacterID)
			if err := s.sendRuntimePartyRosterLocal(joiner, joinerState, "party_invite_join_authoritative_roster"); err != nil {
				return err
			}
			var slotZeroCharID uint16
			if len(joinerState.Members) > 0 {
				// 权威名单是 leader-first：0 号位就是队长（客户端以第一行判队长）。
				slotZeroCharID = joinerState.Members[0].UserID
			}
			s.logGameEvent(joiner, "game-party-join-authoritative-snapshot-sent",
				"type", typ,
				"char_id", selectedCharacterID(joiner),
				"target_id", selectedCharacterID(anchor),
				"party_id", state.PartyID,
				"leader_char_id", state.UserID,
				"member_count", len(state.Members),
				"slot_zero_char_id", slotZeroCharID)
		}
	}
	return nil
}

func (s *Service) handleOnlineEntryIntoParty(session *gameSession, typ uint16, body []byte) error {
	parsed, err := party.DecodeEntryIntoPartyRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-party-entry-body-short", "type", typ, "body_len", len(body), "error", err)
		return s.sendPartyCommandFailure(session, typ, entryIntoPartyFailureGeneric)
	}
	if session == nil || session.selectedCharacterID == 0 || parsed.TargetID == 0 || parsed.TargetID > 0xffff {
		s.logGameEvent(session, "game-party-entry-invalid-context",
			"type", typ,
			"source_char_id", selectedCharacterID(session),
			"target_id", parsed.TargetID)
		return s.sendPartyCommandFailure(session, typ, entryIntoPartyFailureInvalid)
	}
	targetID := uint16(parsed.TargetID)
	if targetID == session.selectedCharacterID {
		s.logGameEvent(session, "game-party-entry-self-rejected", "type", typ, "char_id", session.selectedCharacterID)
		return s.sendPartyCommandFailure(session, typ, entryIntoPartyFailureInvalid)
	}
	targetSession, ok := s.onlineGameSession(targetID)
	if !ok {
		s.logGameEvent(session, "game-party-entry-target-offline",
			"type", typ,
			"source_char_id", session.selectedCharacterID,
			"target_id", targetID)
		return s.sendPartyCommandFailure(session, typ, entryIntoPartyFailureGeneric)
	}
	// op706 is emitted by the existing party owner while it attaches the
	// selected target. Its source is therefore the anchor/leader, unlike the
	// op8 invite response where the source is the accepting joiner.
	state, result, ok := s.createOrJoinManagedRuntimeParty(targetSession, session)
	if !ok {
		// 频道隔离（彻底隔离口径）：跨频道入队拒绝时回**通用失败码**（而不是"队伍已满"
		// 的 20），并记明 reason=party_member_channel_mismatch。
		if result.Reason == partyMemberChannelMismatchReason {
			return s.answerPartyCommandRejection(session, typ, "entry_into_party", entryIntoPartyFailureGeneric,
				"source_char_id", session.selectedCharacterID,
				"target_id", targetID,
				"source_channel", currentTownPresenceChannelID(session),
				"target_channel", currentTownPresenceChannelID(targetSession),
				"reason", partyMemberChannelMismatchReason)
		}
		s.logGameEvent(session, "game-party-entry-party-full",
			"type", typ,
			"source_char_id", session.selectedCharacterID,
			"target_id", targetID,
			"reason", result.Reason)
		return s.sendPartyCommandFailure(session, typ, entryIntoPartyFailureFull)
	}
	if err := s.sendGameUpperRawClassCodec(session, typ, party.BuildEntryIntoPartyAck(parsed.TargetID, uint32(session.selectedCharacterID)), dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if err := s.sendManagedRuntimePartySnapshots(state); err != nil {
		return err
	}
	// 归属判据（只加日志字段、不动一个字节的线格式）：op705 是"直接入队"，
	// 本实现把**发包方**当 anchor/队长（见本函数开头的 op706 注释）、把**包里的 target**
	// 当 joiner。这条归属口径在仓库里没有实测证据，而操作者文档报的正是
	// "被邀请者会成为队长 / 队长无法移交"——如果真实客户端把 op705 当作
	// "我要加入 target 的队伍"（与 op90 队伍目录快速加入同一个方向：
	// `handleRuntimePartyDirectoryJoin` 传的是 `(joiner=发包方, anchor=target)`），
	// 那么这里的 socket/包 target 方向就是反的。把锚点、入队者、最终队长与 0 号位
	// 全打出来，操作者点一次"申请入队"就能单行判读，不必再猜。
	anchorID, joinerID := session.selectedCharacterID, targetID
	slotZeroCharID := uint16(0)
	if members := runtimePartyMembers(state); len(members) > 0 {
		slotZeroCharID = members[0].UserID
	}
	s.logGameEvent(session, "game-party-entry-linked-online-sessions",
		"type", typ,
		"source_char_id", session.selectedCharacterID,
		"target_id", targetID,
		"anchor_char_id", anchorID,
		"joiner_char_id", joinerID,
		"leader_char_id", state.UserID,
		"slot_zero_char_id", slotZeroCharID,
		"member_count", len(state.Members),
		"wire_contract", "op705_entry_into_party_packet_sender_is_the_anchor_unproven_direction")
	return nil
}

func (s *Service) handleOnlineEntryIntoPartyFinish(session *gameSession, typ uint16) error {
	state := runtimePartyStateSnapshot(session)
	if state.PartyID > 0 && len(state.Members) > 1 {
		userState := state.UserState
		if userState == 0 {
			userState = 1
		}
		return s.sendGameUpperRawClassCodec(session, typ, party.BuildEntryIntoPartyFinishEmptyBody(userState), 0, false)
	}
	s.logGameEvent(session, "game-party-entry-finish-missing-context", "type", typ, "source_char_id", selectedCharacterID(session))
	// Current EXE registers only the class0 state/count reader for op706. It has
	// no class1 failure envelope, so the old {0,error} reply would be parsed as
	// state/count and consume bytes from following packets.
	return nil
}

func isPartyInviteResponse(req party.ResponsePeerRequest) bool {
	return req.Mode == 0 || req.Mode == 4 || req.Mode == 12 || req.Mode == 13
}

func isForwardedPeerRequestMode(mode byte) bool {
	return mode == 0 || mode == 1 || mode == 4 || mode == 12 || mode == 13
}

func peerResponseRejected(req party.ResponsePeerRequest) bool {
	// Current EXE sub_269FBF0 accepts ordinary mode-0/1/12 prompts by sending
	// value zero. Quick-party modes 4/13 use explicit value 1 for accept and
	// value 0 for reject (sub_2114B10/sub_2114BB0).
	return (req.Mode == 4 || req.Mode == 13) && req.Value == 0
}

func (s *Service) sendPartyCommandFailure(session *gameSession, typ uint16, code byte) error {
	return s.sendGameUpperRawClassCodec(session, typ, []byte{0, code}, dnfproto.DefaultChannelClassification, true)
}

func (s *Service) sendRuntimePartySnapshot(session *gameSession, state alignedcmd.PartyState) error {
	return s.sendRuntimePartySnapshotMode(session, state, s.partyPeerEndpointsAfterRoster())
}

// sendRuntimePartySnapshotWithoutPeerEndpointRepeat 是 M2（收 CS op2 后重播整份队伍信息）
// 专用的入口：**无论 M1 档位如何都只发一次 op11**，对齐参考实现 `BroadcastPartyInfo`
// 的单次 `0x99 → 0x0B → 0x09`，让 M1/M2 能各自单独判读。
func (s *Service) sendRuntimePartySnapshotWithoutPeerEndpointRepeat(session *gameSession, state alignedcmd.PartyState) error {
	return s.sendRuntimePartySnapshotMode(session, state, partyPeerEndpointsAfterRosterOff)
}

func (s *Service) sendRuntimePartySnapshotMode(
	session *gameSession,
	state alignedcmd.PartyState,
	peerEndpointsAfterRoster partyPeerEndpointsAfterRosterMode,
) error {
	if err := s.sendRuntimePartySnapshotLocal(session, state, true, peerEndpointsAfterRoster); err != nil {
		return err
	}
	return s.broadcastCurrentPartyActorProjection(session, state, s.partyFormationOrder().areaPeerSource())
}

// sendRuntimePartyRosterRefresh is the same local roster plus area-peer
// projection as sendRuntimePartySnapshotMode, but omits the formation-only
// op11 endpoint handshake. It is reserved for mutations of an already formed
// party (currently CHANGE_HOST), where replaying the handshake can reset a
// healthy peer transport and adds a second client-side window update.
func (s *Service) sendRuntimePartyRosterRefresh(
	session *gameSession,
	state alignedcmd.PartyState,
	source string,
) error {
	if err := s.sendRuntimePartySnapshotLocal(session, state, false, partyPeerEndpointsAfterRosterOff); err != nil {
		return err
	}
	return s.broadcastCurrentPartyActorProjection(session, state, source)
}

// sendFormationRealtimeInfo 发 formation 里的 op153（0x0099 实时表），并留下一条
// `game-runtime-party-realtime-info-send` 事件：**字节与改动前逐字相同**（同一个
// `runtimePartyRealtimeInfoBody` + 同一个 codec），只是多一行日志，让"op153 在 op9 之前
// 还是之后"这件事在实测日志里可直接判读（厂商把这一段记为 phase2）。
func (s *Service) sendFormationRealtimeInfo(
	session *gameSession,
	state alignedcmd.PartyState,
	order partyFormationOrderMode,
	source string,
) error {
	body := runtimePartyRealtimeInfoBody(state)
	s.logPacketEvent("game-runtime-party-realtime-info-send",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"member_count", len(runtimePartyMembers(state)),
		"body_len", len(body),
		"msg_id", 0x0099,
		"classification", 0,
		"ordering", order.orderingLabel(),
		"source", source)
	return s.sendGameUpperRawClassCodec(session, 0x0099, body, 0, true)
}

func (s *Service) sendRuntimePartySnapshotLocal(
	session *gameSession,
	state alignedcmd.PartyState,
	sendPeerEndpoints bool,
	peerEndpointsAfterRoster partyPeerEndpointsAfterRosterMode,
) error {
	partyActive := state.PartyID > 0 && len(runtimePartyMembers(state)) > 0
	order := s.partyFormationOrder()
	// bootstrapPeerEndpoints 是 roster **之前**那份 op11 body：M1 的 repeat 档要在 op9
	// 之后把它"再应用一次"，并断言重建结果与它逐字节相同（见 party_peer_endpoints_after_roster.go）。
	var bootstrapPeerEndpoints []byte
	var beforeFrame func() error
	if partyActive && (sendPeerEndpoints || order.realtimeBeforeRoster()) {
		beforeFrame = func() error {
			// vendor / vendor_wire：op11 是 phase1 barrier，先于任何 op9（也先于 op153）。
			if sendPeerEndpoints && order.op11First() {
				body, err := s.sendRuntimePartyPeerEndpointInfo(session, state, order)
				if err != nil {
					return err
				}
				bootstrapPeerEndpoints = body
			}
			// vendor / legacy：op153 在 op9 之前（本仓库既有不变式，见
			// `sendRuntimePartyRosterLocal` 的注释）。vendor_wire 档改到 op9 之后发。
			if order.realtimeBeforeRoster() {
				if err := s.sendFormationRealtimeInfo(session, state, order, order.phase2Source()); err != nil {
					return err
				}
			}
			// legacy：`op153 → op11 → op9`，逐字节回退档。
			if sendPeerEndpoints && !order.op11First() {
				body, err := s.sendRuntimePartyPeerEndpointInfo(session, state, order)
				if err != nil {
					return err
				}
				bootstrapPeerEndpoints = body
			}
			return nil
		}
	}
	frameSent, err := s.sendCurrentPartyActorFrameProjectionBefore(
		session,
		session,
		state,
		order.phase2Source(),
		beforeFrame,
	)
	if err != nil {
		return err
	}
	if !frameSent {
		return nil
	}
	if !partyActive {
		return s.sendGameUpperRawClassCodec(session, 0x0099, party.BuildEmptyRealtimeInfo(), 0, true)
	}
	// vendor_wire：厂商日志里 op153 实际落在 op9 之后（备用档，默认不启用）。
	if !order.realtimeBeforeRoster() {
		if err := s.sendFormationRealtimeInfo(session, state, order, order.phase2Source()); err != nil {
			return err
		}
	}
	// M1（默认 off = 一个字节都不多发）：A21 在 roster 之后再补发一次同一份端点信息，
	// 让绑定落在**最终**成员对象上而不是 roster 之前的临时对象上（D6）。
	if !sendPeerEndpoints {
		return nil
	}
	return s.sendRuntimePartyPeerEndpointsAfterRoster(session, state, bootstrapPeerEndpoints, peerEndpointsAfterRoster)
}

// sendRuntimePartyRosterLocal refreshes the current client's scene-owned
// party tables. The client consumes op153 before op9: op153 carries the
// member HP and stable slot index used by both the party frame and minimap.
// Sending only op9 after rebuilding a town actor leaves the EXE with an old
// (or empty) realtime cache, which presents a zero-HP peer and omits its
// minimap marker until another complete party operation happens.
// The dynamic UDP endpoints in op11 are a party-formation handshake, not a
// general scene refresh: replaying op11 during every town/selector transition
// makes the current client tear down a healthy peer link and enter
// "connecting" again. Therefore scene reconstruction is strictly
// op153-then-op9, without an op11 re-handshake.
func (s *Service) sendRuntimePartyRosterLocal(session *gameSession, state alignedcmd.PartyState, source string) error {
	if err := s.sendRuntimePartyRealtimeInfoLocal(session, state); err != nil {
		return err
	}
	_, err := s.sendCurrentPartyActorFrameProjection(session, session, state, source)
	return err
}

func (s *Service) buildRuntimePartyPeerEndpointInfo(state alignedcmd.PartyState) []byte {
	return s.buildRuntimePartyPeerEndpointInfoForMembers(state, runtimePartyMembers(state))
}

// buildRuntimePartyPeerEndpointInfoForMembers 是直连形态（无中继 / 中继不可用）的 op11 body：
// 只由传入的成员表产出 22B 行，行内 IPv4 用 TCP 观察到的地址、端口用客户端自报端口。
// 与改动前唯一的差别是**成员表由调用方给出**（默认档会先把收包者自己去掉，见
// `partyPeerEndpointRowsExcludeReceiver`）。
func (s *Service) buildRuntimePartyPeerEndpointInfoForMembers(
	state alignedcmd.PartyState,
	members []alignedcmd.PartyMemberState,
) []byte {
	endpoints := make([]party.PeerEndpoint, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		endpoint := party.PeerEndpoint{
			UserID:    member.UserID,
			IPv4:      [4]byte{127, 0, 0, 1},
			Port:      defaultPartyPeerPort,
			AccountID: uint32(member.UserID),
		}
		if peer, ok := s.onlineGameSession(member.UserID); ok && peer.conn != nil {
			registration := currentPartyPeerEndpointSnapshot(peer)
			if registration.Port != 0 {
				endpoint.Port = registration.Port
			}
			if tcpAddress, ok := peer.conn.RemoteAddr().(*net.TCPAddr); ok {
				if ipv4 := tcpAddress.IP.To4(); len(ipv4) == net.IPv4len {
					copy(endpoint.IPv4[:], ipv4)
				}
			}
		}
		endpoints = append(endpoints, endpoint)
	}
	// body 出口统一走 buildPartyPeerEndpointInfoBody：legacy 档在这里逐字节回到
	// `party.BuildPeerEndpointInfo`，vendor 档（默认）盖上厂商 acc/nat/mtu/char_attr，
	// 见 party_peer_row_vendor_fields.go。
	return s.buildPartyPeerEndpointInfoBody(endpoints)
}

// buildRuntimePartyPeerEndpointInfoForReceiver preserves the current EXE's
// PARTY_IP_INFO layout while selecting a directed UDP relay port for every
// remote party member.
//
// 行集合由 `DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF` 决定：
//   - **默认 `off` = 厂商 9.16 形状**：body = `u8 对端数 + 22B/对端行`，
//     **不含收包者自己那一行**（厂商实测 body_len=23、peer_count=1，见
//     party_peer_endpoint_include_self.go 文件头）；
//   - `on` = 我们改动前的形状（含自己那一行，那一行写什么由
//     `DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT` 决定）。
//
// 端口一律按**网络序（大端）**写进 22B 行（与 A21 `PartyIpInfoBuilder.cs:32-33/97-98` 一致）；
// CS op2 的**读**字节序另由 `DNFBRIDGE_PARTY_PEER_PORT_ENDIAN` 决定（默认大端 ⇒ 原样回显）。
//
// 对端行的地址/端口来源由 `DNFBRIDGE_PARTY_LAN_DIRECT` 决定（**默认 relay = 逐字节改动前行为**）：
// 默认档永远取 `relay.Endpoint(...)` 的中继端点；`lan` 档在"两端自报 inner 相同 + 服务端
// 观测到的两端 outer 相同"时改写为对端的 inner 地址 + 自报端口（同局域网直连，见
// party_lan_direct.go 的安全条件与假阳性护栏），其余情况仍走中继端点。
func (s *Service) buildRuntimePartyPeerEndpointInfoForReceiver(receiver *gameSession, state alignedcmd.PartyState) []byte {
	members := runtimePartyMembers(state)
	if receiver == nil || receiver.selectedCharacterID == 0 {
		return s.buildRuntimePartyPeerEndpointInfoForMembers(state, members)
	}
	// 默认档把收包者自己从行集合里去掉（厂商形状）；队伍里只有自己时给出 count=0 的 body，
	// 并留一条可判读的事件（厂商日志里没有单人样本，这一条是唯一的现场判据）。
	rowsMembers, selfOnly := partyPeerEndpointRowsExcludeReceiver(
		members,
		receiver.selectedCharacterID,
		s.partyPeerEndpointIncludeSelf(),
	)
	if selfOnly {
		s.logGameEvent(receiver, "game-runtime-party-peer-endpoints-self-only",
			"char_id", receiver.selectedCharacterID,
			"party_id", state.PartyID,
			"member_count", len(members),
			"peer_count", 0,
			"include_self", s.partyPeerEndpointIncludeSelf().String(),
			"source", "dnfbridge_party_peer_endpoint_include_self_off_no_peer_rows")
	}
	if !s.syncRuntimePartyUDPRelay(state) {
		return s.buildRuntimePartyPeerEndpointInfoForMembers(state, rowsMembers)
	}
	// 后端由档位决定：remote（默认，厂商 a21-relay 控制通道）或 embedded（自建内嵌中继）。
	// 两者同形，端点与端口都来自"该后端当前可用的端口矩阵"；见 party_udp_relay_remote.go。
	relay := s.partyRelayBackend()
	if relay == nil {
		return s.buildRuntimePartyPeerEndpointInfoForMembers(state, rowsMembers)
	}
	selfMode := s.partyPeerSelfEndpoint()
	endpoints := make([]party.PeerEndpoint, 0, len(rowsMembers))
	rows := make([]currentPartyPeerEndpointRow, 0, len(rowsMembers))
	// useRows 只在"自己那一行真的换成了自报 endpoint"时为 true；否则整份 body 仍然由
	// 已证实的 party.BuildPeerEndpointInfo 产出（on 档默认路径逐字节不变）。
	useRows := false
	for _, member := range rowsMembers {
		if member.UserID == 0 {
			continue
		}
		endpoint := party.PeerEndpoint{
			UserID:    member.UserID,
			IPv4:      [4]byte{127, 0, 0, 1},
			Port:      defaultPartyPeerPort,
			AccountID: uint32(member.UserID),
		}
		if member.UserID != receiver.selectedCharacterID {
			// 同局域网直连档（`DNFBRIDGE_PARTY_LAN_DIRECT=lan`，**默认 relay = 本分支不生效**）：
			// 两端自报 inner 相同且服务端观测到的 outer 也相同时，对端行改写为对端的 inner 地址
			// + 自报端口，绕开必须穿过 NAT hairpinning 的中继端点。行布局/字节序一个字节都不改，
			// 只换"写进这一行的地址+端口对"；条件不成立时**原样**落到下面的中继端点。
			// 安全条件（含假阳性护栏）与边界见 party_lan_direct.go。
			if lanAddress, lanPort, observed, isLAN := s.partyLANDirectPeerEndpoint(receiver, member.UserID); isLAN {
				endpoint.IPv4 = lanAddress
				endpoint.Port = lanPort
				endpoints = append(endpoints, endpoint)
				rows = append(rows, peerEndpointRowFromEndpoint(endpoint))
				s.logPartyLANDirectEndpoint(receiver, state, member.UserID, lanAddress, lanPort, observed)
				continue
			}
			if address, port, ok := relay.Endpoint(uint16(state.PartyID), receiver.selectedCharacterID, member.UserID); ok {
				endpoint.IPv4 = address
				endpoint.Port = port
				endpoints = append(endpoints, endpoint)
				rows = append(rows, peerEndpointRowFromEndpoint(endpoint))
				continue
			}
			// Sync is transactional, so reaching this fallback means shutdown or a
			// concurrent room teardown. Publish the previous direct form for the
			// entire snapshot rather than mixing a partial relay topology.
			return s.buildRuntimePartyPeerEndpointInfoForMembers(state, rowsMembers)
		}
		if selfMode.reported() {
			// 自报档优先用收包者**自己**这次会话的 CS op2 上报（与旧路径查到的
			// session 是同一个对象；这里直接取 receiver 以免多一次查找）。
			// 只在 include_self=on（回退档）时才会走到：默认档自己那一行已被去掉。
			if row, ok := currentPartyPeerSelfEndpointRow(
				currentPartyPeerEndpointSnapshot(receiver), member.UserID, uint32(member.UserID)); ok {
				useRows = true
				rows = append(rows, row)
				continue
			}
		}
		if peer, ok := s.onlineGameSession(member.UserID); ok && peer.conn != nil {
			registration := currentPartyPeerEndpointSnapshot(peer)
			if registration.Port != 0 {
				endpoint.Port = registration.Port
			}
			if tcpAddress, ok := peer.conn.RemoteAddr().(*net.TCPAddr); ok {
				if ipv4 := tcpAddress.IP.To4(); len(ipv4) == net.IPv4len {
					copy(endpoint.IPv4[:], ipv4)
				}
			}
		}
		endpoints = append(endpoints, endpoint)
		rows = append(rows, peerEndpointRowFromEndpoint(endpoint))
	}
	if useRows {
		// vendor 档（默认）在这一条出口盖上厂商四字段；legacy 档原样返回 rows，
		// 于是下面这一行与改动前逐字节相同。
		return buildCurrentPartyPeerEndpointInfoRows(s.applyPartyPeerRowVendorFields(rows))
	}
	return s.buildPartyPeerEndpointInfoBody(endpoints)
}

func (s *Service) currentPartyUDPRelay() *currentPartyUDPRelay {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	relay := s.partyUDPRelay
	s.mu.Unlock()
	return relay
}

// syncRuntimePartyUDPRelay ensures that the directed relay set exactly
// matches the current live party. It refuses to use a relay until every party
// member still owns the selected TCP session and that session has an observed
// IPv4 endpoint; the caller then safely falls back to the proven LAN form.
//
// 后端由 `DNFBRIDGE_PARTY_UDP_RELAY_MODE` 决定（默认 remote = 注册到独立的 a21-relay；
// `embedded` = 我们自建的内嵌中继）：remote 档下 `Sync` 拿不到端口矩阵时同样返回 false，
// 调用方按厂商语义**整份回退直连名单**（不做半份混合）。见 party_udp_relay_remote.go。
func (s *Service) syncRuntimePartyUDPRelay(state alignedcmd.PartyState) bool {
	relay := s.partyRelayBackend()
	if state.PartyID > 0 && state.PartyID < int(^uint16(0)) {
		manager := s.runtimePartyManagerForService()
		snapshot, found := manager.SnapshotByID(uint16(state.PartyID))
		if !found {
			return false
		}
		state = snapshot.StateFor(snapshot.Leader)
	}
	members := runtimePartyMembers(state)
	if relay == nil || !relay.Enabled() || state.PartyID <= 0 || len(members) < 2 {
		return false
	}
	bindings := make([]currentPartyUDPRelayBinding, 0, len(members))
	for _, member := range members {
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok || identity.session == nil || identity.generation == 0 || identity.session.conn == nil {
			return false
		}
		tcpAddress, ok := identity.session.conn.RemoteAddr().(*net.TCPAddr)
		if !ok || tcpAddress.IP == nil {
			return false
		}
		ipv4 := tcpAddress.IP.To4()
		if len(ipv4) != net.IPv4len {
			return false
		}
		binding := currentPartyUDPRelayBinding{characterID: identity.character, generation: identity.generation}
		copy(binding.address[:], ipv4)
		// advertisedPort 只喂"反向腿未学习时的回落猜测"（默认关闭的单变量实验，见
		// party_udp_relay_endpoint_fallback.go）。它取自该会话 CS op2 的自报端口，
		// 不参与源校验、也不决定目标 IP。
		binding.advertisedPort = currentPartyPeerEndpointSnapshot(identity.session).Port
		bindings = append(bindings, binding)
	}
	return relay.Sync(uint16(state.PartyID), bindings)
}

func (s *Service) closeRuntimePartyUDPRelay(partyID int) {
	if partyID <= 0 || partyID > int(^uint16(0)) {
		return
	}
	// 与 sync 侧同一个后端：remote 档下这是给 a21-relay 发 CLOSE 交还租约（不等 30s 自然到期）。
	if relay := s.partyRelayBackend(); relay != nil {
		relay.CloseParty(uint16(partyID))
	}
}

func (s *Service) sendRuntimePartyRealtimeInfoLocal(session *gameSession, state alignedcmd.PartyState) error {
	if session == nil || state.PartyID <= 0 || len(runtimePartyMembers(state)) <= 1 {
		return nil
	}
	return s.sendGameUpperRawClassCodec(session, 0x0099, runtimePartyRealtimeInfoBody(state), 0, true)
}

// runtimePartyRealtimeInfoBody 是 op153（0x0099 队员血条/槽位实时表）的 bridge 出口。
//
// 客户端把 op153 与 op9 名册按**同一条队伍槽位顺序**解释：同族实现（S4A21
// `PartyRealtimeInfoBuilder` 与 `PartyInfoNotiBuilder`）两者都取自同一个
// `party.MembersBySlot()`，写进 op153 的 slot 字节就是成员自己的 `SlotIndex`。
// 本仓库模块侧的 `party.BuildSingleMemberRealtimeInfo` 写成员自己的 `Slot`；
// bridge 侧在发送前统一用 `runtimePartyMembers` 的权威顺序。全零旧调用仍按
// leader-first 兼容顺序补成 0..n-1；固定槽位状态则按 `Slot` 排序并保留空洞。
// op153 与 op9 使用同一份顺序，避免委任队长或调整位置后把血条绑定到错误行。
func runtimePartyRealtimeInfoBody(state alignedcmd.PartyState) []byte {
	return party.BuildSingleMemberRealtimeInfo(canonicalRuntimePartyStateForRealtimeInfo(state))
}

// canonicalRuntimePartyStateForRealtimeInfo 把队伍状态的成员顺序对齐成 op9 名册用的那一份。
func canonicalRuntimePartyStateForRealtimeInfo(state alignedcmd.PartyState) alignedcmd.PartyState {
	canonical := cloneRuntimePartyState(state)
	if members := runtimePartyMembers(state); len(members) > 0 {
		canonical.Members = members
	}
	return canonical
}

func (s *Service) broadcastRuntimePartyMemberRemoved(previousState, nextState alignedcmd.PartyState, removedID uint16, skip *gameSession) error {
	members := runtimePartyMembers(nextState)
	if nextState.PartyID <= 0 {
		members = runtimePartyMembers(previousState)
	}
	for _, member := range members {
		if member.UserID == removedID {
			continue
		}
		session, ok := s.onlineGameSession(member.UserID)
		if !ok || session == skip {
			continue
		}
		storeRuntimePartyState(session, nextState)
		if err := s.sendRuntimePartyMemberRemoved(session, removedID, nextState); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sendRuntimePartyMemberRemoved(session *gameSession, _ uint16, state alignedcmd.PartyState) error {
	frameSent, err := s.sendCurrentPartyFrameProjection(session, state, "runtime_party_member_removed")
	if err != nil {
		return err
	}
	if !frameSent {
		return nil
	}
	realtime := party.BuildEmptyRealtimeInfo()
	if state.PartyID > 0 {
		realtime = runtimePartyRealtimeInfoBody(state)
	}
	if err := s.sendGameUpperRawClassCodec(session, 0x0099, realtime, 0, true); err != nil {
		return err
	}
	if state.PartyID <= 0 {
		return s.broadcastCurrentPartyActorProjection(session, state, "runtime_party_member_removed")
	}
	return s.broadcastCurrentPartyActorProjection(session, state, "runtime_party_member_removed")
}

func (s *Service) sendCurrentPartyFrameProjection(session *gameSession, state alignedcmd.PartyState, source string) (bool, error) {
	return s.sendCurrentPartyActorFrameProjection(session, session, state, source)
}

func (s *Service) sendCurrentPartyActorFrameProjection(receiver *gameSession, actor *gameSession, state alignedcmd.PartyState, source string) (bool, error) {
	return s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, source, nil, currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerSession})
}

// currentPartyFrameOwnerMode 选择 op9 记录第 7 字节（owner）的来源：
//   - session：会话里记录着的 town owner（城镇默认；城镇里就等于客户端频道）；
//   - clientChannel：收包者**当前所选频道**；
//   - dungeonContext：副本场景上下文（`currentSceneObjectContext` = 0）；
//   - explicit：调用方给出的字节（副本队友 A/B 开关，见 partyDungeonPeerOwnerPlan）。
//
// 客户端 `sub_1D64CA0` 用"记录 owner 字节 vs 客户端实际所选频道"决定成员行带不带
// **跨频道身份块**：owner != 客户端频道 → 每行带内联身份块（job/level/grow/name）。
type currentPartyFrameOwnerMode int

const (
	currentPartyFrameOwnerSession currentPartyFrameOwnerMode = iota
	currentPartyFrameOwnerClientChannel
	currentPartyFrameOwnerDungeonContext
	// currentPartyFrameOwnerExplicit 用 Owner 字段里的字节。它服务
	// DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的 channel / mixed 两档（副本内发给其他队员的
	// op9）；默认 zero 时所有调用点仍走 currentPartyFrameOwnerDungeonContext，
	// 线格式与日志都不变。mixed 档的 op9 字节仍是 0（与 zero **逐字节相同**），只是
	// 走显式来源以便在日志里打出档位。
	currentPartyFrameOwnerExplicit
)

func (mode currentPartyFrameOwnerMode) String() string {
	switch mode {
	case currentPartyFrameOwnerClientChannel:
		return "client_channel"
	case currentPartyFrameOwnerDungeonContext:
		return "dungeon_context"
	case currentPartyFrameOwnerExplicit:
		return "explicit_owner"
	default:
		return "session_town_owner"
	}
}

// currentPartyFrameOwnerSelection 是一次 op9 组包的 owner 选择。
type currentPartyFrameOwnerSelection struct {
	// Mode 是字节来源种类。
	Mode currentPartyFrameOwnerMode
	// Owner 是 Mode == currentPartyFrameOwnerExplicit 时写进 body[7] 的字节。
	Owner byte
	// Switch 非空 = DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的档位（zero/channel/mixed）：
	// 它覆盖 `owner_mode` 日志字段，让线上在**这一行**就能判出哪一档生效。
	Switch string
	// PeerActorOwner 仅日志：同一次"发给队友"的 op2 mode0/mode1 的 owner（body[4]）。
	// 只有 Switch 非空（副本队友路径）时随日志输出，便于和 body[7] 对照。
	PeerActorOwner byte
}

func (selection currentPartyFrameOwnerSelection) logOwnerMode() string {
	if selection.Switch != "" {
		return selection.Switch
	}
	return selection.Mode.String()
}

// sendCurrentPartyActorFrameProjectionInDungeonContext 用副本场景上下文（0）当 op9 记录的
// owner：客户端因此对成员行走"跨频道身份块"分支（副本内队友 actor 才会被认），
// 而 mode0/mode1 的 owner 0 在客户端与 owner=收包者频道走的是同一条 current-scene 分支。
// 详见 dungeon_party_copresence.go 的副本队友 actor 投影。
func (s *Service) sendCurrentPartyActorFrameProjectionInDungeonContext(receiver *gameSession, actor *gameSession, state alignedcmd.PartyState, source string) (bool, error) {
	return s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, source, nil, currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerDungeonContext})
}

// sendCurrentPartyActorFrameProjectionForDungeonPeer 是副本内**发给其他队员**的 op9 记录：
// owner 字节按 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 的三档决定（见 partyDungeonPeerOwnerPlan）：
//   - zero（默认）：owner = 副本场景上下文 0 ⇒ 与加开关前**逐字节相同**，日志也不变；
//   - channel：owner = 收包者当前频道号（成员行因此不带内联身份块）；
//   - mixed：owner 仍是 0（字节与 zero 相同），档位只体现在日志与 op2 上。
//
// **本地角色自己那条记录一个字节都不改**：receiver 与 actor 是同一个角色时直接走原
// 路径（副本上下文 0），开关只覆盖"发给他人的"包。
func (s *Service) sendCurrentPartyActorFrameProjectionForDungeonPeer(receiver *gameSession, actor *gameSession, state alignedcmd.PartyState, source string) (bool, error) {
	if receiver == nil || actor == nil || actor.selectedCharacterID == receiver.selectedCharacterID {
		return s.sendCurrentPartyActorFrameProjectionInDungeonContext(receiver, actor, state, source)
	}
	plan := s.partyDungeonPeerOwnerPlan(receiver)
	if !plan.overridesPeerOwner() {
		// zero 档（或收包者频道未提交）：走原路径，字节与日志都与现行为一致。
		return s.sendCurrentPartyActorFrameProjectionInDungeonContext(receiver, actor, state, source)
	}
	s.logDungeonPartyOwnerOverride(receiver, actor.selectedCharacterID, plan, source)
	return s.sendCurrentPartyActorFrameProjectionInContext(
		receiver,
		actor,
		state,
		source,
		nil,
		currentPartyFrameOwnerSelection{
			Mode:           currentPartyFrameOwnerExplicit,
			Owner:          plan.PartyFrameOwner,
			Switch:         plan.Mode,
			PeerActorOwner: plan.ActorOwner,
		},
	)
}

func (s *Service) sendCurrentPartyActorFrameProjectionBefore(
	receiver *gameSession,
	actor *gameSession,
	state alignedcmd.PartyState,
	source string,
	beforeFrame func() error,
) (bool, error) {
	return s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, source, beforeFrame, currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerSession})
}

func (s *Service) sendCurrentPartyActorFrameProjectionInContext(
	receiver *gameSession,
	actor *gameSession,
	state alignedcmd.PartyState,
	source string,
	beforeFrame func() error,
	ownerSelection currentPartyFrameOwnerSelection,
) (bool, error) {
	receiverIdentity, ok := s.boundGameSessionCharacterSnapshot(receiver)
	if !ok {
		return false, nil
	}
	actorIdentity, ok := s.boundGameSessionCharacterSnapshot(actor)
	if !ok {
		return false, nil
	}
	receiverOwnerChannel := receiver.townActorOwnerChannel
	// 2026-09-27：默认分支原本直接用会话里**存下的** owner 字段（常驻频道 10 ✗），
	// 与客户端当前频道（252）不一致 ⇒ 客户端 mode0-owner-compare 对远端 actor 行无限重试
	// （渲染被拖死 + 占位面板常驻）。开关打开时统一改走"客户端当前频道"，
	// 与 currentPartyFrameOwnerClientChannel 分支同一原则。
	if currentOwnerContextUsesClientChannel() {
		receiverOwnerChannel = currentClientSelectedChannel(receiver)
	}
	switch ownerSelection.Mode {
	case currentPartyFrameOwnerClientChannel:
		receiverOwnerChannel = currentClientSelectedChannel(receiver)
	case currentPartyFrameOwnerDungeonContext:
		receiverOwnerChannel = currentSceneObjectContext
	case currentPartyFrameOwnerExplicit:
		receiverOwnerChannel = ownerSelection.Owner
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := actorIdentity.character
	_, characterName, character, hasCharacter := s.characterForEnter(ctx, actorIdentity.session, characterID)
	if characterID == 0 {
		return false, nil
	}
	members := runtimePartyMembers(state)
	partyActive := state.PartyID > 0
	projections := make([]currentSceneOp9PartyMemberProjection, 0, len(members))
	projectionIdentities := make([]boundGameSessionCharacter, 0, len(members))
	for _, member := range members {
		memberIdentity := actorIdentity
		if member.UserID != characterID {
			var found bool
			memberIdentity, found = s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !found {
				continue
			}
		}
		memberID, memberName, memberCharacter, memberHasCharacter := s.characterForEnter(
			ctx,
			memberIdentity.session,
			memberIdentity.character,
		)
		if memberID != member.UserID {
			continue
		}
		memberLevel := byte(1)
		memberJob := byte(0)
		memberGrow := byte(0)
		if memberHasCharacter {
			if memberCharacter.Level > 0 && memberCharacter.Level < 256 {
				memberLevel = byte(memberCharacter.Level)
			}
			memberJob = byte(numericCharacterStat(memberCharacter.Job))
			memberGrow = characterGrowTypeByte(memberCharacter)
		}
		projections = append(projections, currentSceneOp9PartyMemberProjection{
			State: member,
			Name:  memberName,
			Job:   memberJob,
			Level: memberLevel,
			Grow:  memberGrow,
		})
		projectionIdentities = append(projectionIdentities, memberIdentity)
	}
	projections = currentSceneOp9PartyMembers(projections)
	// op9 后槽位 4 字节里的第二个字节是**客户端画队长标记用的槽位**
	// （当前 EXE `sub_1D64CA0` 存进该 actor 的队伍记录并交给 `sub_26A0300`）。
	// 现场（2026-09-24 22:53 双开，packet_log.20260924-225314.txt）：名单本身完全正确
	// （op153 `02 | 0300 64 00 00 | 0500 64 00 01`；op9 行 `slot0 uid=3`、`slot1 uid=5`），
	// 但这一字节被写成了"本条记录描述的 actor 自己的槽位"——被邀请方（角色 5）自己那条
	// 记录于是说"队长在第 1 行（0 基）"，两个客户端收到的最后一条队伍记录都指向第 2 行，
	// 客户端就把队长标记画在被邀请人那一行；被邀请方也因此自认队长（能点"委任队长"、
	// 发 op121，线上已被服务端判 sender_is_not_leader 拒绝）。
	// 修法：这一字节必须等于**权威队长所在的行**（`state.UserID`），不伪造任何身份归属。
	leaderSlot := byte(0)
	if partyActive {
		// 记录必须描述名单里的一个真实成员，否则这一帧没有意义（保持原有 fail-closed）。
		if _, actorFound := currentSceneOp9PartyMemberSlot(projections, characterID); !actorFound {
			return false, fmt.Errorf(
				"current party projection missing projected actor %d in party %d",
				characterID,
				state.PartyID,
			)
		}
		leaderSlot = currentSceneOp9PartyLeaderSlot(projections, state.UserID)
	}
	slotZeroCharID := uint16(0)
	if len(projections) > 0 {
		slotZeroCharID = projections[0].State.UserID
	}
	if !s.boundGameSessionCharacterCurrent(receiverIdentity) {
		s.logPacketEvent("game-current-party-frame-op9-deferred",
			"source", source,
			"char_id", characterID,
			"party_id", state.PartyID,
			"reason", "receiving_session_character_changed")
		return false, nil
	}
	if !s.boundGameSessionCharacterCurrent(actorIdentity) {
		s.logPacketEvent("game-current-party-frame-op9-deferred",
			"source", source,
			"char_id", characterID,
			"party_id", state.PartyID,
			"reason", "projected_actor_session_character_changed")
		return false, nil
	}
	for _, identity := range projectionIdentities {
		if s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		s.logPacketEvent("game-current-party-frame-op9-deferred",
			"source", source,
			"char_id", characterID,
			"party_id", state.PartyID,
			"member_id", identity.character,
			"reason", "projected_member_session_character_changed")
		return false, nil
	}
	objectKey := currentSceneActorObjectKey(characterID)
	if !partyActive {
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(
			objectKey,
			receiverOwnerChannel,
			s.currentSceneActorRoute(),
		)
		s.logPacketEvent("game-current-party-frame-op9-remove-send",
			"conn_id", receiver.connID,
			"source", source,
			"receiver_char_id", receiverIdentity.character,
			"char_id", characterID,
			"object_key", objectKey,
			"owner_channel", receiverOwnerChannel,
			"party_id", state.PartyID,
			"body_len", len(body),
			"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
			"classification", 0,
			"body_source", "current_exe_sub_1D64CA0_kind3_party_object_destroy")
		if err := s.sendGameUpperRawClass(receiver, uint16(dnfenum.CmdPacketRecoverStamina), body, 0); err != nil {
			return false, err
		}
		return true, nil
	}
	// 成员行是否带内联身份块，参照值必须是**客户端当前频道**（`sub_1D64CA0`/
	// `sub_269EE50` 的判据），不是本地 town 上下文：进副本后 `townActorOwnerChannel`
	// 被置 0，而客户端频道仍是登录频道，两者相同的话这个分支永远不会命中，
	// 客户端会把我们的 6 字节成员行当"跨频道身份块"读并越界（双开进图 50% 闪退）。
	//
	// 2026-09-27：这个判据必须用**未截断**的 int 频道号（客户端场景频道可能是 508
	// 这种 >255 的值，任何 u8 都不等于它 ⇒ 那些行必须一律带身份块）。
	localChannelID := currentClientSelectedChannelID(receiver)
	body := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(
		objectKey,
		character,
		hasCharacter,
		characterName,
		receiverOwnerChannel,
		localChannelID,
		projections,
		leaderSlot,
		s.options.currentClientVersion == "official-24.9",
		s.currentSceneActorRoute(),
	)
	// The current party transport establishes the realtime roster and P2P
	// endpoints before op9 binds the scene-side member table. Sending op9 first
	// leaves the client in its "connecting" state and prevents teammate map
	// projection from becoming authoritative.
	if beforeFrame != nil {
		if err := beforeFrame(); err != nil {
			return false, err
		}
	}
	op9Fields := []any{
		"conn_id", receiver.connID,
		"source", source,
		"receiver_char_id", receiverIdentity.character,
		"char_id", characterID,
		"object_key", objectKey,
		"owner_channel", receiverOwnerChannel,
		"local_channel", localChannelID,
		"member_identity_block", int(receiverOwnerChannel) != localChannelID,
		"owner_mode", ownerSelection.logOwnerMode(),
		"party_id", state.PartyID,
		"party_active", partyActive,
		"slot_count", len(projections),
		"party_leader_slot", leaderSlot,
		"slot_zero_char_id", slotZeroCharID,
		"leader_char_id", state.UserID,
		"body_len", len(body),
		"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
		"classification", 0,
		"body_source", "current_exe_sub_1D64CA0_kind0_party_slot_table_with_cross_channel_identity_branch",
	}
	if ownerSelection.Switch != "" {
		// 副本队友 A/B 路径：把档位与"同一次发给队友的 op2 owner"一并打出来，
		// 线上在同一行即可判出 zero/channel/mixed，不必对照另一条日志。
		op9Fields = append(op9Fields,
			"owner_switch", ownerSelection.Switch,
			"owner_source", ownerSelection.Mode.String(),
			"op2_owner_channel", ownerSelection.PeerActorOwner)
	}
	s.logPacketEvent("game-current-party-frame-op9-send", op9Fields...)
	if err := s.sendGameUpperRawClass(receiver, uint16(dnfenum.CmdPacketRecoverStamina), body, 0); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) broadcastCurrentPartyActorProjection(actor *gameSession, state alignedcmd.PartyState, source string) error {
	if s == nil || s.onlinePlayers == nil || actor == nil || actor.selectedCharacterID == 0 {
		return nil
	}
	for _, peer := range s.onlinePlayers.PeersInSameArea(actor.selectedCharacterID) {
		if peer.Session == nil {
			continue
		}
		// 与城镇同屏投影同一口径：观察方与该 actor 已在同一支队伍时，这条"再声明一次队伍"的
		// op9 是多余的，而且现场实测会让队员客户端自己 op13 退队（见
		// town_copresence_party_state.go 的 DNFBRIDGE_TOWN_COPRESENCE_SKIP_SAME_PARTY_FRAME）。
		if s.shouldSkipTownCopresencePartyFrame(peer.Session, actor) {
			s.logPacketEvent("game-party-actor-projection-party-frame-skipped",
				"conn_id", peer.Session.connID,
				"source", source+"_to_area_peer",
				"receiver_char_id", peer.Session.selectedCharacterID,
				"actor_char_id", actor.selectedCharacterID,
				"reason", "receiver_and_actor_share_the_same_runtime_party",
				"switch", townCopresenceSkipSamePartyFrameEnv)
			continue
		}
		if _, err := s.sendCurrentPartyActorFrameProjection(peer.Session, actor, state, source+"_to_area_peer"); err != nil {
			return err
		}
	}
	return nil
}

func runtimePartyMember(session *gameSession, state alignedcmd.PartyState) alignedcmd.PartyMemberState {
	member := alignedcmd.PartyMemberState{
		UserID:    session.selectedCharacterID,
		UserState: 1,
		HPPercent: 100,
		MPPercent: 100,
	}
	for _, existing := range state.Members {
		if existing.UserID == session.selectedCharacterID {
			member.Slot = existing.Slot
			if existing.UserState != 0 {
				member.UserState = existing.UserState
			}
			if existing.HPPercent != 0 {
				member.HPPercent = existing.HPPercent
			}
			if existing.MPPercent != 0 {
				member.MPPercent = existing.MPPercent
			}
			break
		}
	}
	return member
}

func hasMultiMemberRuntimeParty(session *gameSession) bool {
	state := runtimePartyStateSnapshot(session)
	return state.PartyID > 0 && len(runtimePartyMembers(state)) > 1
}

func runtimePartyStateSnapshot(session *gameSession) alignedcmd.PartyState {
	if session == nil {
		return alignedcmd.PartyState{}
	}
	session.party.mu.Lock()
	defer session.party.mu.Unlock()
	return cloneRuntimePartyState(session.party.state)
}

func storeRuntimePartyState(session *gameSession, state alignedcmd.PartyState) {
	if session == nil {
		return
	}
	session.party.mu.Lock()
	defer session.party.mu.Unlock()
	state.Members = runtimePartyMembers(state)
	session.party.state = cloneRuntimePartyState(state)
}

func cloneRuntimePartyState(state alignedcmd.PartyState) alignedcmd.PartyState {
	state.NameBytes = append([]byte(nil), state.NameBytes...)
	state.Members = append([]alignedcmd.PartyMemberState(nil), state.Members...)
	return state
}

func runtimePartyMemberBySlot(state alignedcmd.PartyState, slot byte) (alignedcmd.PartyMemberState, bool) {
	members := runtimePartyMembers(state)
	for _, member := range members {
		if member.Slot == slot {
			return member, true
		}
	}
	return alignedcmd.PartyMemberState{}, false
}

func runtimePartyMembers(state alignedcmd.PartyState) []alignedcmd.PartyMemberState {
	maxMembers := state.MaxMembers
	if maxMembers == 0 || maxMembers > 4 {
		maxMembers = 4
	}
	members := make([]alignedcmd.PartyMemberState, 0, int(maxMembers))
	for _, member := range state.Members {
		members = appendRuntimePartyMember(members, member, maxMembers)
	}
	if runtimePartyMembersHaveFixedSlots(members) {
		members = normalizeRuntimePartyMemberSlots(members, maxMembers)
		sort.SliceStable(members, func(i, j int) bool { return members[i].Slot < members[j].Slot })
		return members
	}
	// Legacy callers that supplied only zero-valued Slot fields keep the
	// historical leader-first projection. States carrying a fixed slot from
	// StateFor follow the slot-preserving branch above; the leader remains a
	// separate identity in the state.
	if state.UserID != 0 {
		for index := 1; index < len(members); index++ {
			if members[index].UserID != state.UserID {
				continue
			}
			leader := members[index]
			copy(members[1:index+1], members[0:index])
			members[0] = leader
			break
		}
	}
	for index := range members {
		members[index].Slot = byte(index)
	}
	return members
}

func runtimePartyMembersHaveFixedSlots(members []alignedcmd.PartyMemberState) bool {
	for _, member := range members {
		if member.Slot != 0 {
			return true
		}
	}
	return false
}

// normalizeRuntimePartyMemberSlots keeps unique fixed slots and assigns the
// first free slot only to duplicates or out-of-range values. Production states
// come from StateFor and are already unique; this protects manual/legacy state
// from collapsing several members onto slot zero.
func normalizeRuntimePartyMemberSlots(members []alignedcmd.PartyMemberState, maxMembers byte) []alignedcmd.PartyMemberState {
	used := make([]bool, int(maxMembers))
	nextFree := byte(0)
	for index := range members {
		slot := members[index].Slot
		if int(slot) >= len(used) || used[slot] {
			for int(nextFree) < len(used) && used[nextFree] {
				nextFree++
			}
			slot = nextFree
			members[index].Slot = slot
		}
		if int(slot) < len(used) {
			used[slot] = true
		}
	}
	return members
}

// runtimePartyMembersBySlot returns the runtime manager's members ordered by
// their assigned slot. The client numbers party-frame rows from zero, so this
// order is what a "change host" request's slot byte refers to.
func runtimePartyMembersBySlot(snapshot party.RuntimePartySnapshot) []party.RuntimePartyMember {
	members := append([]party.RuntimePartyMember(nil), snapshot.Members...)
	sort.SliceStable(members, func(i, j int) bool { return members[i].Slot < members[j].Slot })
	return members
}

// runtimePartySnapshotMemberBySlot finds one member by its exact manager slot.
func runtimePartySnapshotMemberBySlot(snapshot party.RuntimePartySnapshot, slot byte) (party.RuntimePartyMember, bool) {
	for _, member := range snapshot.Members {
		if member.Slot == slot {
			return member, true
		}
	}
	return party.RuntimePartyMember{}, false
}

// runtimePartySlotList renders "slot:charID" pairs for diagnostics.
func runtimePartySlotList(snapshot party.RuntimePartySnapshot) string {
	ordered := runtimePartyMembersBySlot(snapshot)
	parts := make([]string, 0, len(ordered))
	for _, member := range ordered {
		parts = append(parts, fmt.Sprintf("%d:%d", member.Slot, member.UserID))
	}
	return strings.Join(parts, ",")
}

func appendRuntimePartyMember(members []alignedcmd.PartyMemberState, member alignedcmd.PartyMemberState, maxMembers byte) []alignedcmd.PartyMemberState {
	if member.UserID == 0 || containsRuntimePartyMember(members, member.UserID) || len(members) >= int(maxMembers) {
		return members
	}
	if member.UserState == 0 {
		member.UserState = 1
	}
	if member.HPPercent == 0 {
		member.HPPercent = 100
	}
	if member.MPPercent == 0 {
		member.MPPercent = 100
	}
	return append(members, member)
}

func containsRuntimePartyMember(members []alignedcmd.PartyMemberState, userID uint16) bool {
	for _, member := range members {
		if member.UserID == userID {
			return true
		}
	}
	return false
}

func selectedCharacterID(session *gameSession) uint16 {
	if session == nil {
		return 0
	}
	return session.selectedCharacterID
}
