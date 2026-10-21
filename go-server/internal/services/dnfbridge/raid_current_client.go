package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

func onlineRaidCommandKnown(op uint16) bool {
	switch dnfenum.CmdPacket(op) {
	case dnfenum.CmdPacketRaidMovieSkip, dnfenum.CmdPacketSelectRaidRewardCard:
		return true
	case dnfenum.CmdPacketRaidEntryCostInfo, dnfenum.CmdPacketCreateRaid, dnfenum.CmdPacketRequestRaidInfo, dnfenum.CmdPacketModifyRaidInfo, dnfenum.CmdPacketLeaveRaid, dnfenum.CmdPacketStartRaid, dnfenum.CmdPacketRejoinRaid, dnfenum.CmdPacketRaidManagerWork, dnfenum.CmdPacketRaidMemberChangeState, dnfenum.CmdPacketRaidOtherChannelList:
		return true
	default:
		return false
	}
}

// Current raid mutations require the actual selected owner and connection
// generation. A cached numeric character ID alone is not authorization.
func (s *Service) currentRaidAuthenticated(session *gameSession) bool {
	if session == nil || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return false
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	repos, available := s.repositoryGroup()
	if !bound || !available || repos.Character == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	return err == nil && found && character.AccountID == s.accountIDForSession(session) && s.boundGameSessionCharacterCurrent(identity)
}

func currentRaidNameValid(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 1020 {
		return false
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return false
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	text := string(decoded)
	if err != nil || !bytes.Equal(raw, encoded) || strings.TrimSpace(text) == "" || len(utf16.Encode([]rune(text))) > 255 {
		return false
	}
	for _, c := range text {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func currentRaidFlagsSupported(flags [3]byte) bool {
	// For Anton/Luke, SingleModeWindow sends argument0 (B4=1) for BUTTON_ONE
	// and argument1 (B4=0) for BUTTON_TWO. B4's native getter selects
	// the single-raid dungeon path; B6 selects a separate phase vector.
	return flags[0] <= 1 && flags[1] == 0 && flags[2] == 0
}

func currentRaidRouteForChannel(channelType int) (byte, bool) {
	switch channelType {
	case dnfenum.RaidChannelType:
		return 0, true
	case dnfenum.HiddenRaidType:
		return 1, true
	case 42:
		return 2, true
	default:
		return 0, false
	}
}

func (s *Service) handleCurrentCreateRaid(session *gameSession, op uint16, body []byte) error {
	req, err := raid.DecodeCurrentCreateRaidRequest(body)
	// Diagnostic-only: preserve the original short-circuit order and failure body.
	// Reasons are fixed literals; never log names, owners, payloads or error text.
	if err != nil {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "request_decode_rejected")
		return s.sendRaidCommandFailure(session, op)
	}
	if !currentRaidNameValid(req.NameBytes) {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "raid_name_rejected")
		return s.sendRaidCommandFailure(session, op)
	}
	if !currentRaidFlagsSupported(req.UIFlags) {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "ui_flags_rejected")
		return s.sendRaidCommandFailure(session, op)
	}
	if !s.currentRaidAuthenticated(session) {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "authentication_rejected")
		return s.sendRaidCommandFailure(session, op)
	}
	route, supported := currentRaidRouteForChannel(int(session.channel.Type))
	if !supported {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "channel_type_unsupported")
		return s.sendRaidCommandFailure(session, op)
	}
	if req.Route != route {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "route_mismatch")
		return s.sendRaidCommandFailure(session, op)
	}
	if route == 2 {
		// 现用EXE 01A55120在超时空直接以参数0打开123创建窗口；
		// 01DF1605因此生成100。超时空没有单人模式，已校验的
		// 000/100均登记并投影为普通模式，后续邀请、改名与入场共用它。
		req.UIFlags[0] = 0
	}
	state, err := s.createCurrentRuntimeRaid(session, req)
	if err != nil {
		s.logGameEvent(session, "game-current-raid-create-rejected", "op", op, "reason", "runtime_create_rejected")
		return s.sendRaidCommandFailure(session, op)
	}
	// The current ACK opens waiting window 0x7A synchronously. Its
	// 01A55500 lookup must already find the actual metadata/roster owner.
	if err := s.sendRuntimeRaidMetadata(session, state, 0); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClassCodec(session, op, raid.BuildCreateRaidResultBody(state.RaidKey), 1, true); err != nil {
		return err
	}
	return s.broadcastRuntimeRaidRefresh(state)
}

func (s *Service) createCurrentRuntimeRaid(session *gameSession, req raid.CurrentInfoRequest) (runtimeRaidState, error) {
	sourceID := selectedCharacterID(session)
	if session == nil || session.channel.ID <= 0 || session.channel.ID > 255 || s.channelAdvertiseID() < 0 || s.channelAdvertiseID() > 255 {
		return runtimeRaidState{}, fmt.Errorf("current raid key owner exceeds native byte fields")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	s.ensureRuntimeRaidMapsLocked()
	if old := s.runtimeRaidByMemberLocked(sourceID); old != nil {
		return runtimeRaidState{}, fmt.Errorf("character already owns raid membership")
	}
	members := s.initialRuntimeRaidMembersLocked(session, sourceID)
	if req.Route == 2 {
		for i := range members {
			members[i].GroupIndex = byte(i + 1)
			members[i].SlotOrder = 0
		}
	}
	if req.UIFlags[0] != 0 && len(members) != 1 {
		return runtimeRaidState{}, fmt.Errorf("single raid has existing party members")
	}
	state := runtimeRaidState{RaidKey: s.nextRuntimeRaidKeyLocked(session, sourceID), LeaderID: sourceID, RouteOrRaidType: req.Route, NameBytes: append([]byte(nil), req.NameBytes...), Members: members, CurrentLayout: true, CurrentUIFlags: req.UIFlags}
	s.raids[state.RaidKey] = &state
	return cloneRuntimeRaidState(state), nil
}

func (s *Service) handleCurrentModifyRaid(session *gameSession, op uint16, body []byte) error {
	req, err := raid.DecodeCurrentModifyRaidInfoRequest(body)
	if err != nil || !currentRaidNameValid(req.NameBytes) || !currentRaidFlagsSupported([3]byte{req.UIFlags[0], req.UIFlags[1], 0}) || !s.currentRaidAuthenticated(session) {
		return s.sendRaidCommandFailure(session, op)
	}
	if req.Route == 2 {
		// 与创建入口保持同一普通超时空模式，不能重新引入单人标志。
		req.UIFlags[0] = 0
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(selectedCharacterID(session))
	if state == nil || !state.CurrentLayout || state.LeaderID != selectedCharacterID(session) || state.Started || state.RouteOrRaidType != req.Route || (state.RouteOrRaidType == 2 && req.UIFlags != [2]byte{}) || (req.UIFlags[0] != 0 && len(state.Members) != 1) {
		s.raidMu.Unlock()
		return s.sendRaidCommandFailure(session, op)
	}
	state.NameBytes = append(state.NameBytes[:0], req.NameBytes...)
	state.CurrentUIFlags[0], state.CurrentUIFlags[1] = req.UIFlags[0], req.UIFlags[1]
	snapshot := cloneRuntimeRaidState(*state)
	s.raidMu.Unlock()
	if err := s.sendGameUpperRawClassCodec(session, op, []byte{1}, 1, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(session, snapshot, 2); err != nil {
		return err
	}
	return s.broadcastRuntimeRaidRefresh(snapshot)
}

func currentRuntimeRaidMembers(state runtimeRaidState) []raid.CurrentMemberRecord {
	core := runtimeRaidMemberRefresh(state).Members
	members := make([]raid.CurrentMemberRecord, len(core))
	for i, record := range core {
		// The current native member reader stores this byte at record+0x38.
		record.Field56 = currentRaidMemberPhaseClearCount(state, record.CharKey)
		// Specialized trailing fields still have no authoritative owner.
		members[i] = raid.CurrentMemberRecord{Core: record}
	}
	return members
}

func currentRuntimeRaidMetadata(state runtimeRaidState) raid.CurrentMetadata {
	members := currentRuntimeRaidMembers(state)
	var leader raid.CurrentMemberRecord
	for _, member := range members {
		if member.Core.CharKey == state.LeaderID {
			leader = member
			break
		}
	}
	// Newly created groups are native state0: 01DF50E0/01DF6B6A only
	// enable a normal leader's start control in this state. State1 is the
	// start transition shown by NOTI587, not the initial recruiting state.
	status := byte(0)
	argument := byte(0)
	if state.Started {
		status = 2
		if b := state.Battle; b != nil {
			argument = b.Phase
			if b.TimedOut {
				status, argument = 4, 1
			} else if b.PhaseCompleted {
				status, argument = 3, 0
				if b.Phase <= 1 && currentRaidRewardFlowSettlementCompleted(b.RewardFlow[b.Phase]) {
					status = 4
					if !b.Single && b.Phase == 0 {
						status = 5
					}
				}
			}
		}
	}
	return raid.CurrentMetadata{RaidKey: state.RaidKey, NameBytes: state.NameBytes, Route: state.RouteOrRaidType, State: status, Phase: argument, Flags: [4]byte{state.CurrentUIFlags[0], state.CurrentUIFlags[1], 0, 0}, Leader: leader, Members: members}
}

func buildCurrentRuntimeRaidMetadata(state runtimeRaidState, mode uint32) ([]byte, error) {
	return raid.BuildCurrentMetadataBody(currentRuntimeRaidMetadata(state), mode)
}

func (s *Service) buildRuntimeRaidRosterBody(state runtimeRaidState) ([]byte, error) {
	if s.options.currentClientVersion == "official-24.9" {
		return raid.BuildCurrentMemberRefreshMode3Body(state.RaidKey, currentRuntimeRaidMembers(state))
	}
	return raid.BuildMemberRefreshMode3Body(runtimeRaidMemberRefresh(state))
}
