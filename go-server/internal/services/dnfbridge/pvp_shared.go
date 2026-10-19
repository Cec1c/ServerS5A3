package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"

	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var errCurrentPVPPeerPending = errors.New("PVP peer UDP endpoint is not ready")

type currentPVPMember struct {
	session           *gameSession
	characterID       uint16
	name              string
	mode0             []byte
	mode1             []byte
	position          currentTownPositionSnapshot
	grade             byte
	currencySlot2     uint32
	rankPoint         uint32
	ratingInitialized bool
	stamina           byte
	job               byte
	grow              byte
	level             int
	total             *currentPVPTotalTeam
}

type currentPVPRoom struct {
	id             uint16
	channelID      int
	request        currentPVPMakeRoomRequest
	battleMode     byte
	manager        byte
	started        bool
	match          *currentPVPMatch
	members        [currentPVPRoomSeatCount]*currentPVPMember
	seatState      [currentPVPRoomSeatCount]byte
	ready          [currentPVPRoomSeatCount]byte
	matchingToken  uint32
	matchingLoaded [currentPVPRoomSeatCount]bool
	arcade         *currentPVPArcadeRun
}

func (room *currentPVPRoom) state() byte {
	if room.started {
		return 2
	}
	return currentPVPRoomState
}

func (room *currentPVPRoom) isPractice() bool {
	return room.battleMode == 6 || room.battleMode == 10
}

func (room *currentPVPRoom) seatFor(session *gameSession) int {
	for seat, member := range room.members {
		if member != nil && member.session == session {
			return seat
		}
	}
	return -1
}

func (s *Service) loadCurrentPVPMember(session *gameSession) (*currentPVPMember, error) {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, name, character, found := s.selectedCharacterForEnter(ctx, session)
	if charID == 0 {
		charID = session.selectedCharacterID
	}
	if !found {
		return nil, fmt.Errorf("PVP character %d is unavailable", charID)
	}
	mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, session, charID, name, character, found, currentSceneObjectContext)
	if err != nil {
		return nil, err
	}
	var repo dnfrepo.LegacyUserInfoRepository
	if repos, ok := s.repositoryGroup(); ok {
		repo = repos.LegacyUserInfo
	}
	peerSkills, err := s.currentPVPPeerSkills(ctx, character)
	if err != nil {
		return nil, err
	}
	pvfStats, hasPVFStats := s.characterPVFStatsForUserInfo(ctx, session, character, found)
	reader := csharpLegacyUserInfoReader{ctx: ctx, repo: repo, characterID: strconv.Itoa(int(charID)),
		service: s, session: session, pvfStats: pvfStats, hasPVFStats: hasPVFStats, peerSkills: peerSkills}
	summary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, found)
	mode1 := reader.buildCurrentUserInfoMode1(character, found, charID, uint32(summary.ManageLevel))
	s.logGameEvent(session, "game-pvp-peer-skills", "character_id", charID, "active_tree", peerSkills[0], "body", fmt.Sprintf("%x", peerSkills))
	session.townMu.Lock()
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	if !position.PositionValid || position.CharacterID != charID {
		return nil, fmt.Errorf("PVP character %d has no committed town position", charID)
	}
	job, err := strconv.Atoi(character.Job)
	if err != nil || job < 0 || job > 255 {
		return nil, fmt.Errorf("invalid PVP character job %q", character.Job)
	}
	return &currentPVPMember{session: session, characterID: charID, name: name, mode0: mode0, mode1: mode1, position: position,
		grade: byte(min(statU32(character, "pvp_grade", 0), 255)), currencySlot2: statU32(character, "finish_loading_currency_slot2_total", 0),
		rankPoint: statU32(character, "pvp_rank_point", 0), stamina: byte(currentPVPWeaknessValue(character.Stats)),
		ratingInitialized: character.Stats["pvp_rating_initialized"] != 0,
		job:               byte(job), grow: byte(statU32(character, "grow_type", 0) & 15), level: character.Level}, nil
}

// ponytail: serialize room changes and publication with one lock; use per-channel
// locks and ordered outbound queues if slow clients affect other channels.
func (s *Service) sendCurrentPVPDirectory(session *gameSession) error {
	if session == nil || session.conn == nil || session.selectedCharacterID == 0 || !isPVPChannel(session.channel) {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	if s.pvpSubscribers == nil {
		s.pvpSubscribers = make(map[*gameSession]int)
	}
	s.pvpSubscribers[session] = session.channel.ID
	ids := make([]int, 0, len(s.pvpRooms))
	for id, room := range s.pvpRooms {
		if room.channelID == session.channel.ID && room.matchingToken == 0 && !room.isPractice() {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		if err := s.sendPVPClass0Packet(session, currentPVPRoomInfoMsgID, buildCurrentPVPRoomInfoBody(s.pvpRooms[uint16(id)]), "pvp_directory_snapshot"); err != nil {
			delete(s.pvpSubscribers, session)
			return err
		}
	}
	s.logGameEvent(session, "game-pvp-directory-sent", "room_count", len(ids))
	return nil
}

func (s *Service) broadcastCurrentPVPPacketLocked(room *currentPVPRoom, exclude *gameSession, msgID uint16, body []byte) {
	if room.matchingToken != 0 || room.isPractice() {
		for _, member := range room.members {
			if member != nil && member.session != exclude {
				if err := s.sendPVPClass0Packet(member.session, msgID, body, "pvp_matching_update"); err != nil {
					s.logGameEvent(member.session, "game-pvp-matching-write-failed", "error", err)
				}
			}
		}
		return
	}
	for peer, channelID := range s.pvpSubscribers {
		if peer == exclude || channelID != room.channelID {
			continue
		}
		if err := s.sendPVPClass0Packet(peer, msgID, body, "pvp_directory_update"); err != nil {
			s.logGameEvent(peer, "game-pvp-directory-write-failed", "room_id", room.id, "error", err)
			delete(s.pvpSubscribers, peer)
		}
	}
}

func (s *Service) enterCurrentPVPRoomLocked(room *currentPVPRoom, seat int, member *currentPVPMember, joining bool) error {
	peers := []*currentPVPMember{member}
	for _, peer := range room.members {
		if peer != nil {
			peers = append(peers, peer)
		}
	}
	endpoints := make(map[*currentPVPMember]party.PeerEndpoint, len(peers))
	if len(peers) > 1 {
		for _, peer := range peers {
			registration := currentPartyPeerEndpointSnapshot(peer.session)
			mtu := currentPartyPeerAdvertisedMTU(registration)
			if registration.Port == 0 || registration.OuterIPv4 == [4]byte{} || mtu == 0 {
				return fmt.Errorf("%w: character %d", errCurrentPVPPeerPending, peer.characterID)
			}
			endpoints[peer] = party.PeerEndpoint{UserID: peer.characterID, AccountID: uint32(peer.characterID), IPv4: currentPVPObservedIPv4(peer.session), Port: registration.Port}
		}
	}
	if err := s.syncPVPRelay(room, peers); err != nil {
		return err
	}
	if s.pvpSubscribers == nil {
		s.pvpSubscribers = make(map[*gameSession]int)
	}
	s.pvpSubscribers[member.session] = room.channelID
	for _, peer := range room.members {
		if peer == nil {
			continue
		}
		for _, exchange := range []struct {
			target *gameSession
			actor  *currentPVPMember
		}{{member.session, peer}, {peer.session, member}} {
			for _, body := range [][]byte{exchange.actor.mode0, exchange.actor.mode1} {
				if err := s.sendGameUpperRawClass(exchange.target, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
					return err
				}
			}
		}
	}
	if err := s.sendGameUpperRawClass(member.session, uint16(dnfenum.CmdPacketSetUDPIPPort), member.mode1, 0); err != nil {
		return err
	}
	// 参考实现调用的是"无条件发送、空列表自动跳过"的版本。本树的
	// sendCurrentSelectedItemLockSnapshot 会**故意**把空列表也发出去（修 op14 之后蓝色
	// 锁图标被重建丢掉的问题，见 item_lock_snapshot.go），所以决斗场这条路径改用本树已有的
	// "仅当有锁定行才发"变体：有锁时两者一致，无锁时两者都不发，PVP 房间包序保持与对方相同，
	// 同时不动非决斗场的锁图标行为。
	if err := s.sendPVPItemLocksIfAny(member.session); err != nil {
		return err
	}
	// sub_1D7FAC0 seeds native endpoints. Both sides need them before op41
	// calls sub_2738AE0/sub_2738060 and starts the reliable-UDP controller.
	for _, target := range peers {
		for _, actor := range peers {
			if actor == target || (target != member && actor != member) {
				continue
			}
			body := s.buildPVPDirectedEndpoint(room, target, actor, endpoints[actor])
			if err := s.sendGameUpperRawClass(target.session, 11, body, 0); err != nil {
				return err
			}
			s.logGameEvent(target.session, "game-pvp-peer-endpoint-sent", "peer", actor.characterID,
				"udp_port", endpoints[actor].Port, "before_room_membership", true)
		}
	}
	room.members[seat] = member
	room.seatState[seat] = 1
	if room.matchingToken == 0 && isCurrentPVPManualBattleMode(room.battleMode) {
		room.seatState[seat] = room.seatStateForMode(seat, 0)
	}
	room.ready[seat] = 0
	member.session.pvpRoom = room
	if room.matchingToken != 0 {
		// Automatic matching publishes its roster after the native search ACK.
		return nil
	}
	info := buildCurrentPVPRoomInfoBody(room)
	if err := s.sendPVPClass0Packet(member.session, currentPVPRoomInfoMsgID, info, "pvp_enter_room"); err != nil {
		return err
	}
	s.broadcastCurrentPVPPacketLocked(room, member.session, currentPVPRoomInfoMsgID, info)
	if room.battleMode == 8 {
		if err := s.refreshCurrentPVPTotalRoomLocked(room); err != nil {
			s.logGameEvent(member.session, "game-pvp-total-roster-pending", "error", err)
		}
	}
	for _, actor := range room.members {
		if actor == nil {
			continue
		}
		p := actor.position
		area := buildCurrentPVPUserAreaBody(actor.characterID, p.PositionX, p.PositionY, p.TownID, p.MovementCode, newCharacterInitialAreaState)
		state, _ := buildCurrentPVPUserStateBody(actor.characterID)
		for _, target := range room.members {
			if target == nil || (target != member && actor != member) {
				continue
			}
			if err := s.sendPVPClass0Packet(target.session, currentPVPUserAreaMsgID, area, "pvp_member_area"); err != nil {
				return err
			}
			if err := s.sendGameUpperRawClass(target.session, uint16(dnfenum.CmdPacketNotifyUserState), state, 0); err != nil {
				return err
			}
			// op2/mode1 carries equipment, not the full-avatar display option.
			// After op41 binds the room model, op357 -> sub_2692CF0 restores
			// user-info+554 and redraws the full skin. Send even default bits=3:
			// a newly constructed user-info object initializes +554 to zero.
			if err := s.sendPVPActorVisibility(target.session, actor.session); err != nil {
				return err
			}
		}
	}
	if joining {
		// sub_1D14F00 stores each ready/HP-percent pair; sub_3447C10 applies
		// that percentage to maximum HP when the opening countdown ends.
		ack := make([]byte, currentPVPRoomSeatCount*2)
		for seat, ready := range room.ready {
			ack[seat*2] = ready
			ack[seat*2+1] = 100
		}
		return s.sendGameUpperSuccess(member.session, uint16(dnfenum.CmdPacketEnterPVPRoom), ack)
	}
	return nil
}

func (s *Service) handleMakePVPRoom(session *gameSession, body []byte, transport string) error {
	if session == nil || session.selectedCharacterID == 0 || !s.freeDuelAvailable() {
		return nil
	}
	request, err := parseCurrentPVPMakeRoomRequest(body)
	if err != nil {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMakePVPRoom), 19)
	}
	session.townMu.Lock()
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	inDojo := position.PositionValid && position.CharacterID == session.selectedCharacterID && position.TownID == 10 && position.AreaID == 4
	if (request.specialBattleMode != 0 && request.specialBattleMode != 1 && request.specialBattleMode != 3) ||
		(request.specialBattleMode == 3 && !inDojo) ||
		(request.specialBattleMode == 0 && (!isPVPChannel(session.channel) || session.channel.Type == 8)) ||
		(!isPVPChannel(session.channel) && !inDojo) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMakePVPRoom), 19)
	}
	member, err := s.loadCurrentPVPMember(session)
	if err != nil {
		return err
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	if session.pvpRoom != nil || hasMultiMemberRuntimeParty(session) {
		return nil
	}
	for _, queued := range s.pvpMatching {
		if queued.member.session == session {
			return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMakePVPRoom), 19)
		}
	}
	if s.pvpRooms == nil {
		s.pvpRooms = make(map[uint16]*currentPVPRoom)
	}
	for id := uint16(1); id < 0x8000; id++ {
		if s.pvpRooms[id] != nil {
			continue
		}
		room := &currentPVPRoom{id: id, channelID: session.channel.ID, request: request, battleMode: currentPVPBattleModeForMake(request)}
		s.pvpRooms[id] = room
		if err := s.enterCurrentPVPRoomLocked(room, 0, member, false); err != nil {
			s.removeCurrentPVPMemberLocked(session, false)
			delete(s.pvpRooms, id)
			return err
		}
		s.logGameEvent(session, "game-pvp-make-room-complete", "room_id", id, "transport", transport)
		return nil
	}
	return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketMakePVPRoom), 4)
}

func (s *Service) handleEnterPVPRoom(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 || !isPVPChannel(session.channel) {
		return nil
	}
	if len(body) < 3 || body[2] > 1 {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 19)
	}
	password := body[3:]
	if body[2] == 1 {
		if len(password) < 4 || binary.LittleEndian.Uint32(password) > 8 || int(binary.LittleEndian.Uint32(password)) != len(password)-4 {
			return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 19)
		}
		password = password[4:]
	} else if len(password) != 0 {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 19)
	}
	member, err := s.loadCurrentPVPMember(session)
	if err != nil {
		return err
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := s.pvpRooms[binary.LittleEndian.Uint16(body)]
	if room == nil || room.matchingToken != 0 || room.isPractice() || room.channelID != session.channel.ID || session.pvpRoom != nil || room.started {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 19)
	}
	if room.request.hasPassword && !bytes.Equal(password, room.request.password) {
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 6)
	}
	for seat, peer := range room.members {
		if peer == nil {
			if err := s.enterCurrentPVPRoomLocked(room, seat, member, true); err != nil {
				s.removeCurrentPVPMemberLocked(session, false)
				if errors.Is(err, errCurrentPVPPeerPending) {
					s.logGameEvent(session, "game-pvp-peer-endpoint-pending", "error", err)
					return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 19)
				}
				return err
			}
			s.logGameEvent(session, "game-pvp-enter-room-complete", "room_id", room.id, "seat", seat)
			return nil
		}
	}
	return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketEnterPVPRoom), 4)
}

func (s *Service) returnCurrentPVPMemberToTown(member *currentPVPMember) error {
	p := member.position
	area := buildCurrentTownUserAreaNotificationBody(member.characterID, p.TownID, p.AreaID, p.PositionX, p.PositionY, p.MovementCode, newCharacterInitialAreaState)
	if err := s.sendPVPClass0Packet(member.session, currentPVPUserAreaMsgID, area, "pvp_leave_room_user_area"); err != nil {
		return err
	}
	transition, err := buildCurrentSceneTransitionBody(p.TownID, p.AreaID, []currentSceneTransitionRow{{member.characterID, p.PositionX, p.PositionY, p.MovementCode, newCharacterInitialAreaState}})
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(member.session, currentSceneTransitionMsgID, transition, 0); err != nil {
		return err
	}
	state, _ := buildCurrentPVPUserStateBodyWithState(member.characterID, 0)
	return s.sendGameUpperRawClass(member.session, uint16(dnfenum.CmdPacketNotifyUserState), state, 0)
}

func (s *Service) removeCurrentPVPMemberLocked(session *gameSession, notifySelf bool) {
	defer s.refreshPVPRelayAfterLeave(session.pvpRoom)
	room := session.pvpRoom
	if room == nil {
		return
	}
	seat := room.seatFor(session)
	if seat < 0 {
		return
	}
	if room.isPractice() {
		if notifySelf {
			// Keep the owner in the room until both private teardown packets
			// have been sent, clearing the native pointer before destruction.
			s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPSeatStateMsgID, buildCurrentPVPSeatStateBody(room.id, room.battleMode, byte(seat), 0xff, 0xffff))
			s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPRoomStateMsgID, buildCurrentPVPDestroyedRoomStateBody(room.id))
		}
		room.members[seat] = nil
		session.pvpRoom = nil
		delete(s.pvpRooms, room.id)
		return
	}
	if room.started {
		if room.match != nil && room.match.total != nil {
			s.ensureGameplayTimeQueue().Cancel(currentPVPTotalPhaseTimerName(room, room.match))
		}
		if room.match != nil && room.match.resultSent {
			s.ensureGameplayTimeQueue().Cancel(currentPVPResultTimerName(room, room.match))
		}
		room.match = nil
		room.started = false
		room.ready = [currentPVPRoomSeatCount]byte{}
		for _, member := range room.members {
			if member == nil || member.session == session {
				continue
			}
			if err := s.returnCurrentPVPMemberToTown(member); err != nil {
				s.logGameEvent(member.session, "game-pvp-match-abort-return-failed", "error", err)
			}
			s.removeCurrentPVPMemberLocked(member.session, true)
		}
	}
	room.members[seat] = nil
	room.ready[seat] = 0
	session.pvpRoom = nil
	exclude := session
	if notifySelf {
		exclude = nil
	}
	// Clear local membership before freeing the native room object on any peer.
	s.broadcastCurrentPVPPacketLocked(room, exclude, currentPVPSeatStateMsgID, buildCurrentPVPSeatStateBody(room.id, room.battleMode, byte(seat), 0xff, 0xffff))
	for next, member := range room.members {
		if member != nil {
			if int(room.manager) == seat {
				room.manager = byte(next)
			}
			s.broadcastCurrentPVPPacketLocked(room, exclude, currentPVPRoomStateMsgID, buildCurrentPVPRoomStateBody(room.id, 1, room.manager, room.request.mapIndex, room.battleMode, 0))
			return
		}
	}
	delete(s.pvpRooms, room.id)
	s.broadcastCurrentPVPPacketLocked(room, exclude, currentPVPRoomStateMsgID, buildCurrentPVPDestroyedRoomStateBody(room.id))
}

func (s *Service) closeCurrentPVPSession(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	delete(s.pvpSubscribers, session)
	s.cancelCurrentPVPMatchingLocked(session)
	if room := session.pvpRoom; room != nil && room.matchingToken != 0 {
		s.closeCurrentPVPMatchingLocked(room, session)
		return
	}
	s.removeCurrentPVPMemberLocked(session, false)
}

func (s *Service) handleSetPVPSeatState(session *gameSession, body []byte, transport string) error {
	if session == nil || len(body) != 2 || body[0] >= currentPVPRoomSeatCount || (body[1] != 0xfe && body[1] > 4) {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || (!isPVPChannel(session.channel) && !room.isPractice()) {
		return nil
	}
	seat := room.seatFor(session)
	if seat < 0 || (body[1] != 0xfe && int(body[0]) != seat) {
		return nil
	}
	if body[1] == 0xfe {
		if room.matchingToken != 0 {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return nil
		}
		if err := s.returnCurrentPVPMemberToTown(room.members[seat]); err != nil {
			return err
		}
		s.removeCurrentPVPMemberLocked(session, true)
		s.logGameEvent(session, "game-pvp-leave-room-complete", "room_id", room.id, "transport", transport)
		return nil
	}
	if room.started || room.matchingToken != 0 || room.isPractice() {
		return nil
	}
	room.seatState[seat] = room.seatStateForMode(seat, body[1])
	s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPSeatStateMsgID, buildCurrentPVPSeatStateBody(room.id, room.battleMode, byte(seat), room.seatState[seat], room.members[seat].characterID))
	s.resetCurrentPVPReadyLocked(room, seat)
	return nil
}

func (s *Service) handleSetPVPTeamMode(session *gameSession, body []byte, transport string) error {
	if session == nil || !isPVPChannel(session.channel) || len(body) != 1 || !isCurrentPVPManualBattleMode(body[0]) {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || room.started || room.matchingToken != 0 || room.isPractice() || room.seatFor(session) != int(room.manager) {
		return nil
	}
	if room.battleMode == body[0] {
		return nil
	}
	previous := room.battleMode
	room.battleMode = normalizeCurrentPVPBattleMode(body[0])
	for seat, member := range room.members {
		if member != nil {
			room.seatState[seat] = room.seatStateForMode(seat, room.seatState[seat])
		}
	}
	s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPSeatStateMsgID, buildCurrentPVPAllSeatStatesBody(room))
	for seat := range room.ready {
		s.resetCurrentPVPReadyLocked(room, seat)
	}
	if room.battleMode == 8 {
		if err := s.refreshCurrentPVPTotalRoomLocked(room); err != nil {
			s.logGameEvent(session, "game-pvp-total-roster-pending", "error", err)
		}
	}
	s.logGameEvent(session, "game-pvp-mode-changed", "room_id", room.id, "previous_mode", previous,
		"battle_mode", room.battleMode, "seat_states", room.seatState, "transport", transport)
	return nil
}

func (room *currentPVPRoom) seatStateForMode(seat int, requested byte) byte {
	personal := room.battleMode == 1 || room.battleMode == 4
	if requested == 3 || requested == 4 {
		if personal {
			return 4
		}
		return 3
	}
	if personal {
		return 0
	}
	if requested == 1 || requested == 2 {
		return requested
	}
	// Preserve valid existing teams. Place an unassigned combatant on the
	// smaller team; vacant seats and observers do not affect the counts.
	red, blue := 0, 0
	for other, member := range room.members {
		if member == nil || other == seat {
			continue
		}
		switch room.seatState[other] {
		case 1:
			red++
		case 2:
			blue++
		}
	}
	if red > blue {
		return 2
	}
	return 1
}

func (s *Service) resetCurrentPVPReadyLocked(room *currentPVPRoom, seat int) {
	wasReady := room.ready[seat] != 0
	room.ready[seat] = 0
	if !wasReady || room.members[seat] == nil {
		return
	}
	// op44 has no room ID, so it must never go to directory subscribers.
	for _, member := range room.members {
		if member != nil {
			if err := s.sendGameUpperRawClass(member.session, 44, []byte{byte(seat), 0}, 0); err != nil {
				s.logGameEvent(member.session, "game-pvp-ready-reset-write-failed", "error", err)
			}
		}
	}
}

func (s *Service) handleSetPVPReadyState(session *gameSession, body []byte) error {
	if session == nil || !isPVPChannel(session.channel) || len(body) != 1 || body[0] > 1 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil {
		return nil
	}
	seat := room.seatFor(session)
	if seat < 0 {
		return nil
	}
	if room.started || room.matchingToken != 0 {
		return nil
	}
	if room.seatState[seat] == 3 || room.seatState[seat] == 4 {
		return nil
	}
	if seat == int(room.manager) && body[0] == 1 {
		return s.startCurrentPVPRoomLocked(room, session)
	}
	room.ready[seat] = body[0]
	for _, member := range room.members {
		if member == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(member.session, 44, []byte{byte(seat), body[0]}, 0); err != nil {
			s.logGameEvent(member.session, "game-pvp-ready-write-failed", "error", err)
		}
	}
	return nil
}

func (s *Service) handleSetPVPMapIndex(session *gameSession, body []byte, transport string) error {
	if session == nil || !isPVPChannel(session.channel) || len(body) != 2 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || room.started || room.matchingToken != 0 || room.seatFor(session) != int(room.manager) {
		return nil
	}
	room.request.mapIndex = int16(binary.LittleEndian.Uint16(body))
	s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPRoomStateMsgID, buildCurrentPVPRoomStateBody(room.id, 1, room.manager, room.request.mapIndex, room.battleMode, 0))
	return nil
}
