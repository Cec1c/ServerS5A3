// raid_online.go 负责当前 game 连接内的攻坚队运行态流程。
// 这里只保存在线会话可见的短生命周期攻坚队，不落 DB；跨服持久 owner 后续再替换该边界。
package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
)

const (
	runtimeRaidDefaultGroup = 1
	runtimeRaidMaxGroups    = 5
	runtimeRaidGroupSize    = 4
	runtimeRaidFailureCode  = 3
)

type runtimeRaidState struct {
	RaidKey         uint32
	LeaderID        uint16
	RouteOrRaidType byte
	NameBytes       []byte
	TailFlag        byte
	Members         []runtimeRaidMemberState
	Started         bool
	CurrentLayout   bool
	CurrentUIFlags  [3]byte
	Battle          *runtimeRaidBattle
}

type runtimeRaidMemberState struct {
	CharID     uint16
	Name       string
	GroupIndex byte
	SlotOrder  byte
	UserState  byte
	HPPercent  byte
	MPPercent  byte
	Job        byte
	GrowType   byte
	Level      uint32
}

func (s *Service) handleOnlineRaidCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	if s.options.currentClientVersion == "official-24.9" {
		if handled, err := s.handleCurrentRaidInvitationCommand(session, typ, body); handled || err != nil {
			return handled, err
		}
		if handled, err := s.handleCurrentRaidApplicationCommand(session, typ, body); handled || err != nil {
			return handled, err
		}
	}
	if s.options.currentClientVersion == "official-24.9" && onlineRaidCommandKnown(typ) && !s.currentRaidAuthenticated(session) {
		if dnfenum.CmdPacket(typ) == dnfenum.CmdPacketCreateRaid {
			s.logGameEvent(session, "game-current-raid-create-rejected", "op", typ, "reason", "dispatch_authentication_rejected")
		}
		return true, s.sendRaidCommandFailure(session, typ)
	}
	switch dnfenum.CmdPacket(typ) {
	case dnfenum.CmdPacketRaidSetSymbol:
		if s.options.currentClientVersion == "official-24.9" {
			if session.channel.Type == dnfenum.HiddenRaidType {
				return true, s.handleCurrentLukeGeneratorSymbol(session, body)
			}
			return true, s.handleCurrentAntonEnergySymbol(session, typ, body)
		}
		return false, nil
	case dnfenum.CmdPacketRaidMovieSkip:
		if s.options.currentClientVersion == "official-24.9" {
			return true, s.handleCurrentAntonMovieCompletion(session, typ, body)
		}
		return false, nil
	case dnfenum.CmdPacketSelectRaidRewardCard:
		if s.options.currentClientVersion == "official-24.9" {
			return true, s.handleCurrentAntonRewardCard(session, typ, body)
		}
		return false, nil
	case dnfenum.CmdPacketRaidEntryCostInfo:
		return true, s.handleOnlineRaidEntryCostInfo(session, typ, body)
	case dnfenum.CmdPacketCreateRaid:
		return true, s.handleOnlineCreateRaid(session, typ, body)
	case dnfenum.CmdPacketRequestRaidInfo:
		return true, s.handleOnlineRequestRaidInfo(session, body)
	case dnfenum.CmdPacketModifyRaidInfo:
		return true, s.handleOnlineModifyRaidInfo(session, typ, body)
	case dnfenum.CmdPacketLeaveRaid:
		return true, s.handleOnlineLeaveRaid(session, typ, body)
	case dnfenum.CmdPacketStartRaid:
		return true, s.handleOnlineStartRaid(session, typ, body)
	case dnfenum.CmdPacketRejoinRaid:
		return true, s.handleOnlineRejoinRaid(session, typ, body)
	case dnfenum.CmdPacketRaidManagerWork:
		return true, s.handleOnlineRaidManagerWork(session, typ, body)
	case dnfenum.CmdPacketRaidMemberChangeState:
		return true, s.handleOnlineRaidMemberChangeState(session, typ, body)
	case dnfenum.CmdPacketRaidOtherChannelList:
		if s.options.currentClientVersion == "official-24.9" {
			return true, s.handleCurrentRaidSearch(session, typ, body)
		}
		return false, nil
	default:
		return false, nil
	}
}

func (s *Service) handleOnlineCreateRaid(session *gameSession, typ uint16, body []byte) error {
	if s.options.currentClientVersion == "official-24.9" {
		return s.handleCurrentCreateRaid(session, typ, body)
	}
	req, err := raid.DecodeCreateRaidRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-create-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	sourceID := selectedCharacterID(session)
	if sourceID == 0 || session == nil || (session.channel.Type != dnfenum.RaidChannelType && session.channel.Type != dnfenum.HiddenRaidType) {
		return s.sendRaidCommandFailure(session, typ)
	}
	state := s.createRuntimeRaid(session, sourceID, req)
	if err := s.sendGameUpperRawClassCodec(session, typ, raid.BuildCreateRaidResultBody(state.RaidKey), dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(session, state, 0); err != nil {
		return err
	}
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		return err
	}
	s.logGameEvent(session, "game-raid-created",
		"type", typ,
		"raid_key", state.RaidKey,
		"leader_id", state.LeaderID,
		"member_count", len(state.Members))
	return nil
}

func (s *Service) sendRuntimeRaidMetadata(session *gameSession, state runtimeRaidState, mode uint32) error {
	if s.options.currentClientVersion == "official-24.9" {
		body, err := buildCurrentRuntimeRaidMetadata(state, mode)
		if err != nil {
			return err
		}
		return s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, body, 0, true)
	}
	refresh := runtimeRaidMemberRefresh(state)
	var leader raid.MemberRecord
	for _, member := range refresh.Members {
		if member.CharKey == state.LeaderID {
			leader = member
			break
		}
	}
	body, err := raid.BuildMetadataBody(raid.Metadata{RaidKey: state.RaidKey, NameBytes: state.NameBytes, Route: state.RouteOrRaidType, TailFlag: state.TailFlag, Started: state.Started, Leader: leader, Members: refresh.Members}, mode)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, body, 0, true)
}

// Captured current-client requests use u8(1), u32 raid key after CREATE_RAID;
// retain mode2 requests supported by the earlier client trace as well.
// Current ACK1359 returns a read-only object/roster. NOTI591 is a membership
// update: its mode3 handler assigns the client's own raid, even for a viewer.
func (s *Service) handleOnlineRequestRaidInfo(session *gameSession, body []byte) error {
	if s.options.currentClientVersion == "official-24.9" && !s.currentRaidAuthenticated(session) {
		return nil
	}
	if len(body) != 5 || (body[0] != 1 && body[0] != 2) {
		return nil
	}
	key := binary.LittleEndian.Uint32(body[1:])
	s.raidMu.Lock()
	stored := s.raids[key]
	var state runtimeRaidState
	if stored != nil {
		state = cloneRuntimeRaidState(*stored)
	}
	s.raidMu.Unlock()
	if state.RaidKey == 0 {
		return nil
	}
	if runtimeRaidMemberIndex(state, selectedCharacterID(session)) < 0 && !s.currentRaidInfoVisible(session, state) {
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" {
		// Retained membership cannot bypass the selected server/channel/route.
		// A freshly restarted member has no local own-raid key. Install the
		// complete object through the actual ACK's idempotent upsert, then
		// publish membership only to this authenticated member. Mode0 NOTI591
		// would increment the directory count again on every repeated query.
		if !s.currentRaidInfoVisible(session, state) {
			return nil
		}
		member := runtimeRaidMemberIndex(state, selectedCharacterID(session)) >= 0
		selector := body[0]
		if member {
			if state.Battle != nil && state.Battle.MemberAccounts[selectedCharacterID(session)] != s.accountIDForSession(session) {
				return nil
			}
			selector = 2
		}
		reply, err := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), selector)
		if err != nil {
			return err
		}
		s.logGameEvent(session, "game-raid-info-sent", "raid_key", key, "mode", "current_ack1359", "selector", selector)
		if err := s.sendCurrentRuntimeRaidProfiles(session, state); err != nil {
			return err
		}
		if err := s.sendGameUpperRawClassCodec(session, uint16(dnfenum.CmdPacketRequestRaidInfo), reply, 1, true); err != nil {
			return err
		}
		if member {
			roster, err := s.buildRuntimeRaidRosterBody(state)
			if err != nil {
				return err
			}
			if err := s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, roster, 0, true); err != nil {
				return err
			}
			return s.sendCurrentRuntimeRaidEntryCost(session, state)
		}
		return nil
	}
	if err := s.sendRuntimeRaidMetadata(session, state, 2); err != nil {
		return err
	}
	body, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-raid-info-sent", "raid_key", key, "mode", "metadata_update_then_real_roster")
	return s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, body, 0, true)
}

// The current search UI asks for the roster before requesting membership.
// This is read-only visibility of an existing directory object, not authority
// to mutate it. Historical layouts retain their members-only behavior.
func (s *Service) currentRaidInfoVisible(session *gameSession, state runtimeRaidState) bool {
	if s.options.currentClientVersion != "official-24.9" || !state.CurrentLayout || session == nil || !s.currentRaidAuthenticated(session) {
		return false
	}
	route, supported := currentRaidRouteForChannel(int(session.channel.Type))
	serverID := s.channelAdvertiseID()
	// 02024200 also checks the key's channel byte before reading NOTI591.
	return supported && route == state.RouteOrRaidType && serverID >= 0 && serverID <= 255 && byte(state.RaidKey>>24) == byte(serverID) && byte(state.RaidKey>>16) == byte(session.channel.ID)
}

func (s *Service) handleOnlineModifyRaidInfo(session *gameSession, typ uint16, body []byte) error {
	if s.options.currentClientVersion == "official-24.9" {
		return s.handleCurrentModifyRaid(session, typ, body)
	}
	req, err := raid.DecodeModifyRaidInfoRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-modify-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	sourceID := selectedCharacterID(session)
	state, ok := s.modifyRuntimeRaidInfo(sourceID, req)
	if !ok {
		return s.sendRaidCommandFailure(session, typ)
	}
	if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	return s.broadcastRuntimeRaidRefresh(state)
}

func (s *Service) handleOnlineLeaveRaid(session *gameSession, typ uint16, body []byte) error {
	if _, err := raid.DecodeLeaveRaidRequest(body); err != nil {
		s.logGameEvent(session, "game-raid-leave-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	sourceID := selectedCharacterID(session)
	wasAntonWaiting := s.currentAntonWaitingAreaPresence(session)
	state, removed, disbanded := s.removeRuntimeRaidMember(sourceID)
	if !removed {
		return s.sendRaidCommandFailure(session, typ)
	}
	// In the current ACK665 reader the flag means forced removal (B6C7),
	// not an empty directory object. Its ordinary branch still executes
	// 01A64B80 and clears both the own-raid key and cached object pointer.
	forcedExit := disbanded && s.options.currentClientVersion != "official-24.9"
	if err := s.sendGameUpperRawClassCodec(session, typ, raid.BuildLeaveRaidResultBody(forcedExit), dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	storeRuntimePartyState(session, alignedcmd.PartyState{})
	if err := s.sendRuntimePartyMemberRemoved(session, sourceID, alignedcmd.PartyState{}); err != nil {
		return err
	}
	if wasAntonWaiting {
		if err := s.returnCurrentAntonMemberToGate(session); err != nil {
			return err
		}
	}
	if disbanded {
		return nil
	}
	// The waiting UI compares metadata.Leader against the local UID; the
	// roster's leader flag alone cannot enable the successor's controls.
	if s.options.currentClientVersion == "official-24.9" && state.CurrentLayout {
		for _, member := range state.Members {
			peer, online := s.onlineGameSession(member.CharID)
			if !online || !s.currentRaidAdmissionVisible(peer, state) {
				continue
			}
			if err := s.sendRuntimeRaidMetadata(peer, state, 2); err != nil {
				return err
			}
		}
	}
	return s.broadcastRuntimeRaidRefresh(state)
}

func (s *Service) handleOnlineStartRaid(session *gameSession, typ uint16, body []byte) error {
	if s.options.currentClientVersion == "official-24.9" && !s.currentRaidAuthenticated(session) {
		return s.sendRaidCommandFailure(session, typ)
	}
	if err := raid.DecodeStartRaidRequest(body); err != nil {
		s.logGameEvent(session, "game-raid-start-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	sourceID := selectedCharacterID(session)
	state, ok, err := s.startRuntimeRaidWithEntryCost(session)
	if err != nil {
		s.logGameEvent(session, "game-raid-start-rejected", "source_char_id", sourceID, "error", err)
	}
	if !ok {
		return s.sendRaidCommandFailure(session, typ)
	}
	if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if s.options.currentClientVersion == "official-24.9" {
		for _, member := range state.Members {
			if peer, found := s.onlineGameSession(member.CharID); found {
				if err := s.sendRuntimeRaidMetadata(peer, state, 2); err != nil {
					return err
				}
			}
		}
	}
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		return err
	}
	if err := s.applyRuntimeRaidParties(state); err != nil {
		return err
	}
	if s.options.currentClientVersion == "official-24.9" {
		for _, member := range state.Members {
			peer, found := s.onlineGameSession(member.CharID)
			if member.CharID == sourceID {
				peer, found = session, true
			}
			if found {
				if err := s.sendCurrentRaidBattleState(peer, state); err != nil {
					return err
				}
			}
		}
	}
	// Reuse the verified container refresh only after a successful group commit.
	// log/off emit exactly the established success sequence without this refresh.
	if isCurrentFiendWarChannel(session) || s.raidStartEntryCostMode() == raidStartEntryCostGuardEnforce {
		for _, member := range state.Members {
			peer, found := s.onlineGameSession(member.CharID)
			if member.CharID == sourceID {
				peer, found = session, true
			}
			if found {
				if err := s.sendSelectedCurrentContainerListsRefresh(peer, "raid_entry_cost_after_commit"); err != nil {
					return err
				}
			}
		}
	}
	s.logGameEvent(session, "game-raid-started",
		"type", typ,
		"raid_key", state.RaidKey,
		"member_count", len(state.Members))
	return nil
}

func (s *Service) handleOnlineRejoinRaid(session *gameSession, typ uint16, body []byte) error {
	req, err := raid.DecodeRejoinRaidRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-rejoin-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return s.sendRaidCommandFailure(session, typ)
	}
	sourceID := selectedCharacterID(session)
	var state runtimeRaidState
	var ok bool
	if s.options.currentClientVersion == "official-24.9" {
		// Rejoin restores only retained membership in the current directory.
		// Validate the snapshot before any success or peer projection; a legacy
		// object must not admit an official client through the old append path.
		state, ok = s.runtimeRaidForMember(sourceID)
		if !ok || state.RaidKey != req.RaidKey || session.channel.ID < 0 || session.channel.ID > 255 || !s.currentRaidInfoVisible(session, state) {
			return s.sendRaidCommandFailure(session, typ)
		}
		if b := state.Battle; b != nil && (b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || b.MemberAccounts[sourceID] != s.accountIDForSession(session)) {
			return s.sendRaidCommandFailure(session, typ)
		}
	} else {
		state, ok = s.rejoinRuntimeRaid(req.RaidKey, sourceID)
		if !ok {
			return s.sendRaidCommandFailure(session, typ)
		}
	}
	if err := s.sendGameUpperRawClassCodec(session, typ, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		return err
	}
	if s.options.currentClientVersion == "official-24.9" && state.Started && state.Battle != nil {
		// Metadata/map/timer restoration must not replay NOTI587's start
		// animation or already completed death/movie instructions.
		if err := s.sendRuntimeRaidMetadata(session, state, 2); err != nil {
			return err
		}
		if err := s.sendCurrentRaidProgressProjection(session, state, false, true); err != nil {
			return err
		}
		if err := s.sendCurrentAntonTerminalResult(session, state); err != nil {
			return err
		}
		return s.sendCurrentAntonRewardFlowProjection(session, state)
	}
	return nil
}

func (s *Service) handleOnlineRaidManagerWork(session *gameSession, typ uint16, body []byte) error {
	req, err := raid.DecodeManagerWorkRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-manager-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return nil
	}
	sourceID := selectedCharacterID(session)
	state, ok, projectionChanged := s.moveRuntimeRaidMember(sourceID, uint16(req.MemberCharKey), byte(req.TargetGroup))
	if !ok {
		s.logGameEvent(session, "game-raid-manager-rejected",
			"type", typ,
			"source_char_id", sourceID,
			"member_char_id", req.MemberCharKey,
			"target_group", req.TargetGroup)
		return nil
	}
	// S2C669 is DoNothing. Publish roster first, then only the changed
	// live participation projection; no phase-start/reward side effects.
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		return err
	}
	if projectionChanged {
		s.queueCurrentRaidRosterProgress(state)
	}
	return nil
}

func (s *Service) handleOnlineRaidMemberChangeState(session *gameSession, typ uint16, body []byte) error {
	req, err := raid.DecodeMemberChangeStateRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-raid-member-state-body-invalid", "type", typ, "body_len", len(body), "error", err)
		return nil
	}
	sourceID := selectedCharacterID(session)
	state, ok := s.changeRuntimeRaidMemberState(sourceID, normalizeRuntimeRaidMemberState(req.State))
	if !ok {
		s.logGameEvent(session, "game-raid-member-state-ignored",
			"type", typ,
			"source_char_id", sourceID,
			"state", req.State)
		return nil
	}
	// MCP/IDA 证据显示 823 是客户端成员状态变化；可见同步走 0x24F 成员名单刷新。
	return s.broadcastRuntimeRaidRefresh(state)
}

func normalizeRuntimeRaidMemberState(state byte) byte {
	if state == 0 {
		return 0
	}
	return 1
}

func (s *Service) createRuntimeRaid(session *gameSession, sourceID uint16, req raid.InfoRequest) runtimeRaidState {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	s.ensureRuntimeRaidMapsLocked()
	s.removeRuntimeRaidMemberLocked(sourceID)
	key := s.nextRuntimeRaidKeyLocked(session, sourceID)
	state := runtimeRaidState{
		RaidKey:         key,
		LeaderID:        sourceID,
		RouteOrRaidType: req.RouteOrRaidType,
		NameBytes:       append([]byte(nil), req.NameBytes...),
		TailFlag:        req.TailFlag,
	}
	state.Members = s.initialRuntimeRaidMembersLocked(session, sourceID)
	s.raids[key] = &state
	return cloneRuntimeRaidState(state)
}

func (s *Service) modifyRuntimeRaidInfo(sourceID uint16, req raid.InfoRequest) (runtimeRaidState, bool) {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil || state.LeaderID != sourceID {
		return runtimeRaidState{}, false
	}
	state.RouteOrRaidType = req.RouteOrRaidType
	state.NameBytes = append(state.NameBytes[:0], req.NameBytes...)
	state.TailFlag = req.TailFlag
	return cloneRuntimeRaidState(*state), true
}

func (s *Service) removeRuntimeRaidMember(sourceID uint16) (runtimeRaidState, bool, bool) {
	s.raidMu.Lock()
	state, removed, disbanded := s.removeRuntimeRaidMemberLocked(sourceID)
	var snapshot runtimeRaidState
	if state != nil {
		snapshot = cloneRuntimeRaidState(*state)
	}
	var others []onlinePlayerInfo
	if removed && s.onlinePlayers != nil {
		if p, found := s.onlinePlayers.PlayerForCharacter(sourceID); found && p.Instance != 0 && s.currentAntonWaitingArea(p.TownID, p.AreaID) {
			_, others = s.onlinePlayers.LeaveArea(sourceID)
		}
	}
	s.raidMu.Unlock()
	if len(others) > 0 {
		s.broadcastTownPlayerLeave(sourceID, others)
	}
	return snapshot, removed, disbanded
}

// runtimeRaidForMember 只读地取该成员所在的攻坚队快照。
func (s *Service) runtimeRaidForMember(sourceID uint16) (runtimeRaidState, bool) {
	if sourceID == 0 {
		return runtimeRaidState{}, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil || len(state.Members) == 0 {
		return runtimeRaidState{}, false
	}
	return cloneRuntimeRaidState(*state), true
}

func (s *Service) startRuntimeRaidWithEntryCost(session *gameSession) (runtimeRaidState, bool, error) {
	sourceID := selectedCharacterID(session)
	// This bounded repository operation must finish before membership can change.
	// Never acquire another raid lock from the transaction callback.
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil || state.LeaderID != sourceID || len(state.Members) == 0 || len(state.Members) > currentRaidMemberMaximum(*state) {
		return runtimeRaidState{}, false, nil
	}
	if state.Started {
		if !s.currentRaidAuthenticated(session) || state.Battle == nil || state.Battle.MemberAccounts[sourceID] != s.accountIDForSession(session) || state.Battle.ChannelID != session.channel.ID || state.Battle.ChannelType != session.channel.Type {
			return runtimeRaidState{}, false, nil
		}
		ok, err := s.beginCurrentAntonNextPhaseLocked(state, time.Now())
		if !ok || err != nil {
			return runtimeRaidState{}, false, err
		}
		return cloneRuntimeRaidState(*state), true, nil
	}
	if runtimeRaidHasOversizedGroup(*state) {
		return runtimeRaidState{}, false, nil
	}
	seen := make(map[uint16]bool, len(state.Members))
	for _, member := range state.Members {
		if member.CharID == 0 || seen[member.CharID] || member.GroupIndex > currentRaidGroupMaximum(*state) {
			return runtimeRaidState{}, false, nil
		}
		// A retained offline seat is not a paid admission. Bind/restore or
		// expiry must resolve it before the current aggregate is frozen.
		if seat, reserved := s.currentRaidDisconnects[member.CharID]; state.CurrentLayout && reserved && seat.Raid == state {
			return runtimeRaidState{}, false, nil
		}
		seen[member.CharID] = true
	}
	battle, err := s.prepareCurrentRaidBattle(session, cloneRuntimeRaidState(*state))
	if err != nil {
		return runtimeRaidState{}, false, err
	}
	var waitingScope currentRaidWaitingBattleScope
	allowed := false
	if battle != nil && battle.FiendWar != nil {
		waitingScope, err = s.prepareCurrentRaidWaitingBattleScope(session, *state, battle)
		if err != nil {
			return runtimeRaidState{}, false, err
		}
		if err := s.onlinePlayers.requireRaidStartWaitingArea(waitingScope, ""); err != nil {
			return runtimeRaidState{}, false, err
		}
		err = s.consumeCurrentFiendWarEntry(session, *state, battle)
		allowed = err == nil
	} else {
		allowed, err = s.guardRaidStartEntryCost(session, cloneRuntimeRaidState(*state))
	}
	if err != nil || !allowed {
		return runtimeRaidState{}, false, err
	}
	state.Started = true
	state.Battle = battle
	if battle != nil {
		if battle.FiendWar != nil {
			s.onlinePlayers.bindRaidWaitingBattleInstance(waitingScope)
		} else {
			s.onlinePlayers.bindAntonWaitingBattleInstance(state.RaidKey, battle.InstanceID)
		}
	}
	return cloneRuntimeRaidState(*state), true, nil
}

func (s *Service) rejoinRuntimeRaid(raidKey uint32, sourceID uint16) (runtimeRaidState, bool) {
	if sourceID == 0 {
		return runtimeRaidState{}, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[raidKey]
	if state == nil {
		return runtimeRaidState{}, false
	}
	if runtimeRaidMemberIndex(*state, sourceID) < 0 {
		// Current rejoin restores an existing member; it is never an alternate
		// way to bypass a leader's approval of a new application.
		if state.CurrentLayout {
			return runtimeRaidState{}, false
		}
		if len(state.Members) >= currentRaidMemberMaximum(*state) {
			return runtimeRaidState{}, false
		}
		state.Members = append(state.Members, s.newRuntimeRaidMemberLocked(sourceID, runtimeRaidDefaultGroup, nextRuntimeRaidSlot(*state, runtimeRaidDefaultGroup)))
	}
	return cloneRuntimeRaidState(*state), true
}

func (s *Service) changeRuntimeRaidMemberState(sourceID uint16, userState byte) (runtimeRaidState, bool) {
	if sourceID == 0 {
		return runtimeRaidState{}, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(sourceID)
	if state == nil {
		return runtimeRaidState{}, false
	}
	index := runtimeRaidMemberIndex(*state, sourceID)
	if index < 0 {
		return runtimeRaidState{}, false
	}
	state.Members[index].UserState = userState
	return cloneRuntimeRaidState(*state), true
}

func (s *Service) moveRuntimeRaidMember(sourceID uint16, memberID uint16, targetGroup byte) (runtimeRaidState, bool, bool) {
	if targetGroup == 0 || targetGroup > 8 {
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
	if targetGroup > currentRaidGroupMaximum(*state) {
		return runtimeRaidState{}, false, false
	}
	if state.RouteOrRaidType == 2 && state.Started {
		return runtimeRaidState{}, false, false
	}
	if state.RouteOrRaidType == 2 {
		for i := range state.Members {
			if state.Members[i].CharID != memberID && state.Members[i].GroupIndex == targetGroup {
				state.Members[i].GroupIndex = 0
				state.Members[i].SlotOrder = 0
			}
		}
	}
	groupChanged := state.Members[index].GroupIndex != targetGroup
	state.Members[index].GroupIndex = targetGroup
	state.Members[index].SlotOrder = nextRuntimeRaidSlotExcluding(*state, targetGroup, memberID)
	runtimeRaidSortMembers(state.Members)
	projectionChanged := false
	if s.options.currentClientVersion == "official-24.9" && groupChanged && state.Started && state.Battle != nil && !state.Battle.TimedOut {
		entry, occupied := state.Battle.Participation[memberID]
		if occupied && entry.RunID != "" && entry.Phase == state.Battle.Phase && state.Battle.MemberAccounts[memberID] != "" && (state.Battle.Anton == nil || entry.Attempt == state.Battle.Anton.Epoch()) {
			state.Battle.ProgressVersion++
			projectionChanged = true
		}
	}
	return cloneRuntimeRaidState(*state), true, projectionChanged
}

func (s *Service) broadcastRuntimeRaidRefresh(state runtimeRaidState) error {
	body, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		return err
	}
	for _, member := range state.Members {
		session, ok := s.onlineGameSession(member.CharID)
		if !ok {
			continue
		}
		if err := s.sendCurrentRuntimeRaidProfiles(session, state); err != nil {
			return err
		}
		if err := s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, body, 0, true); err != nil {
			return err
		}
		if err := s.sendCurrentRuntimeRaidEntryCost(session, state); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyRuntimeRaidParties(state runtimeRaidState) error {
	// Permanent FiendWar seats are not ordinary parties. Admission creates the
	// temporary combat party from the confirmed source-map roster.
	if state.Battle != nil && state.Battle.FiendWar != nil {
		return nil
	}
	groupStates := make([]alignedcmd.PartyState, 0)
	for _, group := range runtimeRaidPartyGroups(state) {
		if len(group) == 0 {
			continue
		}
		partyState, replaced := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(state, group))
		if !replaced {
			return fmt.Errorf("raid party group could not be applied through runtime party manager")
		}
		groupStates = append(groupStates, partyState)
	}
	// Any prior ordinary party that still has non-raid survivors was changed by
	// the central manager while group members moved. Refresh its projections
	// from manager snapshots before writing any client-facing raid roster.
	s.publishAllManagedRuntimeParties()
	for _, partyState := range groupStates {
		for _, member := range runtimePartyMembers(partyState) {
			session, ok := s.onlineGameSession(member.UserID)
			if !ok {
				continue
			}
			if err := s.sendRuntimePartySnapshot(session, partyState); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) initialRuntimeRaidMembersLocked(session *gameSession, sourceID uint16) []runtimeRaidMemberState {
	members := []runtimeRaidMemberState{s.newRuntimeRaidMemberLocked(sourceID, runtimeRaidDefaultGroup, 0)}
	state := runtimePartyStateSnapshot(session)
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == sourceID || len(members) >= raid.MaxAttackPartyMembers {
			continue
		}
		members = append(members, s.newRuntimeRaidMemberLocked(member.UserID, runtimeRaidDefaultGroup, byte(len(members)%runtimeRaidGroupSize)))
	}
	return members
}

func (s *Service) newRuntimeRaidMemberLocked(charID uint16, group byte, slot byte) runtimeRaidMemberState {
	member := runtimeRaidMemberState{
		CharID:     charID,
		Name:       s.runtimeRaidCharacterName(charID),
		GroupIndex: group,
		SlotOrder:  slot,
		UserState:  1,
		HPPercent:  100,
		MPPercent:  100,
	}
	if repos, ok := s.repositoryGroup(); ok && repos.Character != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		if character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(charID))); err == nil && found {
			member.Name = character.Name
			member.Job = byte(numericCharacterStat(character.Job))
			member.GrowType = characterGrowTypeByte(character)
			member.Level = uint32(rosterLevel(character))
		}
	}
	return member
}

func (s *Service) runtimeRaidCharacterName(charID uint16) string {
	repos, ok := s.repositoryGroup()
	if ok && repos.Character != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		if record, found, err := repos.Character.Load(ctx, strconv.Itoa(int(charID))); err == nil && found && record.Name != "" {
			return record.Name
		}
	}
	return csharpSelectCharacterName(charID)
}

func (s *Service) sendRaidCommandFailure(session *gameSession, typ uint16) error {
	return s.sendGameUpperRawClassCodec(session, typ, []byte{0, runtimeRaidFailureCode}, dnfproto.DefaultChannelClassification, true)
}

func (s *Service) ensureRuntimeRaidMapsLocked() {
	if s.raids == nil {
		s.raids = make(map[uint32]*runtimeRaidState)
	}
}

func (s *Service) nextRuntimeRaidKeyLocked(session *gameSession, sourceID uint16) uint32 {
	channelID := uint32(0)
	if session != nil {
		channelID = uint32(uint16(session.channel.ID))
	}
	prefix := channelID << 16
	if s.options.currentClientVersion == "official-24.9" {
		// Current 02024200 checks bits16..23 against global56A1E80
		// (CHANNELINFO field8: selected channel), and bits24..31 against
		// global56A1E74 (field7: advertised server) before parsing 591.
		prefix = channelID<<16 | uint32(s.channelAdvertiseID())<<24
	}
	base := prefix | uint32(sourceID)
	if base != 0 {
		if _, exists := s.raids[base]; !exists {
			return base
		}
	}
	for {
		s.nextRaidSeq++
		key := prefix | uint32(uint16(s.nextRaidSeq))
		if key == 0 {
			continue
		}
		if _, exists := s.raids[key]; !exists {
			return key
		}
	}
}

func (s *Service) runtimeRaidByMemberLocked(charID uint16) *runtimeRaidState {
	if charID == 0 {
		return nil
	}
	for _, state := range s.raids {
		if runtimeRaidMemberIndex(*state, charID) >= 0 {
			return state
		}
	}
	return nil
}

func (s *Service) removeRuntimeRaidMemberLocked(charID uint16) (*runtimeRaidState, bool, bool) {
	state := s.runtimeRaidByMemberLocked(charID)
	if state == nil {
		return nil, false, false
	}
	next := state.Members[:0]
	removed := false
	for _, member := range state.Members {
		if member.CharID == charID {
			removed = true
			continue
		}
		next = append(next, member)
	}
	if !removed {
		return state, false, false
	}
	if b := state.Battle; b != nil && b.FiendWar != nil {
		f := b.FiendWar.Clone()
		delete(f.MemberAreas, charID)
		delete(f.RecoveryUntil, charID)
		delete(f.GroupAreas, b.MemberGroups[charID])
		b.FiendWar = f
		b.ProgressVersion++
	}
	state.Members = next
	if len(state.Members) == 0 {
		delete(s.raids, state.RaidKey)
		return nil, true, true
	}
	if state.LeaderID == charID || runtimeRaidMemberIndex(*state, state.LeaderID) < 0 {
		state.LeaderID = state.Members[0].CharID
	}
	return state, true, false
}

func runtimeRaidMemberRefresh(state runtimeRaidState) raid.MemberRefresh {
	members := append([]runtimeRaidMemberState(nil), state.Members...)
	runtimeRaidSortMembers(members)
	out := raid.MemberRefresh{RaidKey: state.RaidKey, Members: make([]raid.MemberRecord, 0, len(members))}
	for _, member := range members {
		out.Members = append(out.Members, raid.MemberRecord{
			CharKey:           member.CharID,
			Field4:            member.UserState,
			Name:              member.Name,
			Field40:           member.Job,
			Field44:           member.GrowType,
			GroupIndex:        member.GroupIndex,
			SlotOrder:         member.SlotOrder,
			Field48:           member.Level,
			Field52:           0,
			Field53:           0,
			Field56:           0,
			Field60:           0,
			Field64:           0,
			Field66BoolSource: boolUint32(member.CharID == state.LeaderID),
		})
	}
	return out
}

func runtimeRaidPartyGroups(state runtimeRaidState) [][]runtimeRaidMemberState {
	members := append([]runtimeRaidMemberState(nil), state.Members...)
	runtimeRaidSortMembers(members)
	groups := make([][]runtimeRaidMemberState, 0, runtimeRaidMaxGroups)
	byGroup := make(map[byte][]runtimeRaidMemberState)
	for _, member := range members {
		group := member.GroupIndex
		if group == 0 {
			group = runtimeRaidDefaultGroup
		}
		member.GroupIndex = group
		byGroup[group] = append(byGroup[group], member)
	}
	keys := make([]int, 0, len(byGroup))
	for group := range byGroup {
		keys = append(keys, int(group))
	}
	sort.Ints(keys)
	for _, key := range keys {
		groups = append(groups, byGroup[byte(key)])
	}
	return groups
}

func runtimeRaidHasOversizedGroup(state runtimeRaidState) bool {
	counts := make(map[byte]int)
	for _, member := range state.Members {
		group := member.GroupIndex
		if group == 0 {
			group = runtimeRaidDefaultGroup
		}
		counts[group]++
		if counts[group] > currentRaidGroupCapacity(state) {
			return true
		}
	}
	return false
}

func runtimeRaidGroupPartyState(state runtimeRaidState, members []runtimeRaidMemberState) alignedcmd.PartyState {
	leaderID := members[0].CharID
	partyState := alignedcmd.PartyState{
		PartyID:    int(state.RaidKey&0xffff) + int(members[0].GroupIndex)*1000,
		IsLeader:   true,
		UserID:     leaderID,
		UserState:  1,
		NameBytes:  append([]byte(nil), state.NameBytes...),
		MaxMembers: runtimeRaidGroupSize,
		Members:    make([]alignedcmd.PartyMemberState, 0, len(members)),
	}
	if len(partyState.NameBytes) == 0 {
		partyState.NameBytes = []byte("raid")
	}
	for _, member := range members {
		partyState.Members = append(partyState.Members, alignedcmd.PartyMemberState{
			UserID:    member.CharID,
			UserState: firstByte(member.UserState, 1),
			HPPercent: firstByte(member.HPPercent, 100),
			MPPercent: firstByte(member.MPPercent, 100),
		})
	}
	return partyState
}

func runtimeRaidMemberIndex(state runtimeRaidState, charID uint16) int {
	for i, member := range state.Members {
		if member.CharID == charID {
			return i
		}
	}
	return -1
}

func nextRuntimeRaidSlot(state runtimeRaidState, group byte) byte {
	return nextRuntimeRaidSlotExcluding(state, group, 0)
}

func nextRuntimeRaidSlotExcluding(state runtimeRaidState, group byte, exclude uint16) byte {
	used := make(map[byte]bool)
	for _, member := range state.Members {
		if member.CharID == exclude || member.GroupIndex != group {
			continue
		}
		used[member.SlotOrder] = true
	}
	for i := byte(0); i < runtimeRaidGroupSize; i++ {
		if !used[i] {
			return i
		}
	}
	return runtimeRaidGroupSize - 1
}

func runtimeRaidSortMembers(members []runtimeRaidMemberState) {
	sort.SliceStable(members, func(i, j int) bool {
		leftGroup := raidSortGroup(members[i].GroupIndex)
		rightGroup := raidSortGroup(members[j].GroupIndex)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		if members[i].SlotOrder != members[j].SlotOrder {
			return members[i].SlotOrder < members[j].SlotOrder
		}
		return members[i].CharID < members[j].CharID
	})
}

func raidSortGroup(group byte) int {
	if group == 0 {
		return 1000
	}
	return int(group)
}

func cloneRuntimeRaidState(state runtimeRaidState) runtimeRaidState {
	state.NameBytes = append([]byte(nil), state.NameBytes...)
	state.Members = append([]runtimeRaidMemberState(nil), state.Members...)
	state.Battle = cloneRuntimeRaidBattle(state.Battle)
	return state
}

func boolUint32(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}

func firstByte(value byte, fallback byte) byte {
	if value != 0 {
		return value
	}
	return fallback
}
