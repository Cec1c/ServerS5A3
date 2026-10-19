package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// DNF sub_39E90B0/sub_39E9410: integrated=1, personVsPerson=0.
type currentPVPMatchingRequest struct {
	gameType   uint16
	member     *currentPVPMember
	generation uint64
	option     byte
	banEnabled bool
	banned     [4][2]byte
	final      byte
}

func parseCurrentPVPMatchingRequest(body []byte) (currentPVPMatchingRequest, error) {
	var request currentPVPMatchingRequest
	if len(body) != 14 || body[0] != 1 || (binary.LittleEndian.Uint16(body[1:]) != 0 && binary.LittleEndian.Uint16(body[1:]) != 7) || body[3] > 1 || body[4] > 1 || body[13] > 1 {
		return request, fmt.Errorf("invalid integrated 1v1 matching request")
	}
	request.gameType = binary.LittleEndian.Uint16(body[1:])
	request.option, request.banEnabled, request.final = body[3], body[4] == 1, body[13]
	for i := range request.banned {
		job, grow := body[5+i*2], body[6+i*2]
		if (job == 255) != (grow == 255) || (job != 255 && (job > 15 || grow > 15)) {
			return request, fmt.Errorf("invalid matching exclusion")
		}
		request.banned[i] = [2]byte{job, grow}
	}
	return request, nil
}

func (s *Service) currentPVPMatchingAvailable(session *gameSession) bool {
	if session == nil || session.conn == nil || session.accountID == "" || session.selectedCharacterID == 0 ||
		!isPVPChannel(session.channel) || session.channel.Type != 8 || session.pvpRoom != nil ||
		hasMultiMemberRuntimeParty(session) {
		return false
	}
	if _, bound := s.boundGameSessionCharacterSnapshot(session); !bound {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return session.dungeon.runtime == nil
}

func (s *Service) cancelCurrentPVPMatchingLocked(session *gameSession) {
	for i := len(s.pvpMatching) - 1; i >= 0; i-- {
		if s.pvpMatching[i].member.session == session {
			s.pvpMatching = append(s.pvpMatching[:i], s.pvpMatching[i+1:]...)
		}
	}
}

func (s *Service) handleCurrentPVPMatching(session *gameSession, opcode uint16, body []byte) error {
	if session == nil || !isPVPChannel(session.channel) || session.channel.Type != 8 {
		return nil
	}
	if opcode == 570 {
		return s.completeCurrentPVPMatching(session, body)
	}
	if opcode == 521 {
		if len(body) != 3 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:]) != currentPVPMatchingGameType(session) {
			return s.sendGameUpperFailure(session, opcode, 1)
		}
		s.pvpMu.Lock()
		defer s.pvpMu.Unlock()
		if room := session.pvpRoom; room != nil && room.matchingToken != 0 && room.started {
			return s.sendGameUpperFailure(session, opcode, 1)
		}
		s.cancelCurrentPVPMatchingLocked(session)
		if err := s.sendGameUpperSuccess(session, opcode, nil); err != nil {
			return err
		}
		if room := session.pvpRoom; room != nil && room.matchingToken != 0 && !room.started {
			s.closeCurrentPVPMatchingLocked(room, nil)
		}
		s.logGameEvent(session, "game-pvp-matching-cancelled")
		return nil
	}
	request, err := parseCurrentPVPMatchingRequest(body)
	if err != nil || request.gameType != currentPVPMatchingGameType(session) {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	if !s.currentPVPMatchingAvailable(session) {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	for _, queued := range s.pvpMatching {
		if queued.member.session == session {
			return s.sendGameUpperSuccess(session, 520, []byte{1, byte(queued.gameType), 0, queued.option})
		}
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	member, err := s.loadCurrentPVPMember(session)
	if err != nil {
		return err
	}
	if request.gameType == 7 {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		member.total, err = s.loadCurrentPVPTotalTeam(ctx, session)
		cancel()
		if err != nil {
			s.logGameEvent(session, "game-pvp-total-matching-rejected", "error", err)
			return s.sendGameUpperFailure(session, opcode, 1)
		}
	}
	request.member, request.generation = member, identity.generation
	if !s.boundGameSessionCharacterCurrent(identity) || !s.currentPVPMatchingAvailable(session) {
		return s.sendGameUpperFailure(session, opcode, 1)
	}
	if err := s.sendGameUpperSuccess(session, 520, []byte{1, byte(request.gameType), 0, request.option}); err != nil {
		return err
	}
	for i := 0; i < len(s.pvpMatching); {
		queued := s.pvpMatching[i]
		peer := queued.member.session
		if queued.generation != peer.characterGeneration || !s.currentPVPMatchingAvailable(peer) {
			s.pvpMatching = append(s.pvpMatching[:i], s.pvpMatching[i+1:]...)
			continue
		}
		if peer.channel.ID != session.channel.ID || peer.accountID == session.accountID ||
			peer.selectedCharacterID == session.selectedCharacterID ||
			queued.gameType != request.gameType || queued.option != request.option || queued.final != request.final ||
			queued.excludes(member) || request.excludes(queued.member) {
			i++
			continue
		}
		s.pvpMatching = append(s.pvpMatching[:i], s.pvpMatching[i+1:]...)
		// Equipment and learned skills may change while the search is open.
		fresh, loadErr := s.loadCurrentPVPMember(peer)
		if loadErr != nil || queued.generation != peer.characterGeneration || !s.currentPVPMatchingAvailable(peer) {
			s.logGameEvent(peer, "game-pvp-matching-stale-peer", "error", loadErr)
			if err := s.sendGameUpperRawClass(peer, 506, []byte{1}, 0); err != nil {
				s.logGameEvent(peer, "game-pvp-matching-close-write-failed", "error", err)
			}
			continue
		}
		if queued.gameType == 7 {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			fresh.total, loadErr = s.loadCurrentPVPTotalTeam(ctx, peer)
			cancel()
			if loadErr != nil {
				if err := s.sendGameUpperRawClass(peer, 506, []byte{1}, 0); err != nil {
					return err
				}
				continue
			}
		}
		queued.member = fresh
		if request.excludes(fresh) {
			s.pvpMatching = append(s.pvpMatching, nil)
			copy(s.pvpMatching[i+1:], s.pvpMatching[i:])
			s.pvpMatching[i] = queued
			i++
			continue
		}
		return s.pairCurrentPVPMatchingLocked(queued, &request)
	}
	s.pvpMatching = append(s.pvpMatching, &request)
	s.logGameEvent(session, "game-pvp-matching-queued", "bound", 1, "game_type", request.gameType)
	return nil
}

func (request *currentPVPMatchingRequest) excludes(member *currentPVPMember) bool {
	if !request.banEnabled {
		return false
	}
	for _, pair := range request.banned {
		if member.total != nil {
			for _, c := range member.total.snapshot.Characters {
				if pair[0] != 255 && pair == [2]byte{byte(numericCharacterStat(c.Job)), byte(c.Stats["grow_type"] & 15)} {
					return true
				}
			}
			continue
		}
		if pair[0] != 255 && pair == [2]byte{member.job, member.grow} {
			return true
		}
	}
	return false
}

func currentPVPMatchingTimerName(room *currentPVPRoom) string {
	return fmt.Sprintf("dnf-pvp-matching:%d", room.matchingToken)
}

func (s *Service) pairCurrentPVPMatchingLocked(a, b *currentPVPMatchingRequest) error {
	if s.pvpRooms == nil {
		s.pvpRooms = make(map[uint16]*currentPVPRoom)
	}
	id := uint16(1)
	for id < 0x8000 && s.pvpRooms[id] != nil {
		id++
	}
	if id == 0x8000 {
		for _, request := range []*currentPVPMatchingRequest{a, b} {
			if err := s.sendGameUpperRawClass(request.member.session, 506, []byte{1}, 0); err != nil {
				s.logGameEvent(request.member.session, "game-pvp-matching-close-write-failed", "error", err)
			}
		}
		return nil
	}
	s.nextPVPMatchingToken++
	if s.nextPVPMatchingToken == 0 || s.nextPVPMatchingToken == 0xffffffff {
		s.nextPVPMatchingToken = 1
	}
	room := &currentPVPRoom{id: id, channelID: a.member.session.channel.ID, battleMode: 2,
		request: currentPVPMakeRoomRequest{roomNameType: 5}, matchingToken: s.nextPVPMatchingToken}
	if a.gameType == 7 {
		room.battleMode = 8
	}
	s.pvpRooms[id] = room
	for seat, request := range []*currentPVPMatchingRequest{a, b} {
		if err := s.enterCurrentPVPRoomLocked(room, seat, request.member, false); err != nil {
			for _, pending := range []*currentPVPMatchingRequest{a, b} {
				if pending.member.session.pvpRoom != room {
					if sendErr := s.sendGameUpperRawClass(pending.member.session, 506, []byte{1}, 0); sendErr != nil {
						s.logGameEvent(pending.member.session, "game-pvp-matching-close-write-failed", "error", sendErr)
					}
				}
			}
			s.closeCurrentPVPMatchingLocked(room, nil)
			if errors.Is(err, errCurrentPVPPeerPending) {
				s.logGameEvent(request.member.session, "game-pvp-peer-endpoint-pending", "error", err)
				return nil
			}
			return err
		}
		room.seatState[seat] = byte(seat + 1)
		room.ready[seat] = 1
	}
	if room.battleMode == 8 {
		if err := s.refreshCurrentPVPTotalRoomLocked(room); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
	}
	// Native op41 installs real actors and op51 seeds HP percentages before
	// the loading scene applies maximum HP (sub_1D14F00/sub_3447C10).
	for _, member := range room.members[:2] {
		if err := s.sendPVPClass0Packet(member.session, 41, buildCurrentPVPRoomInfoBody(room), "pvp_matching_roster"); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
		ack := make([]byte, currentPVPRoomSeatCount*2)
		for seat := range room.ready {
			ack[seat*2], ack[seat*2+1] = room.ready[seat], 100
		}
		if err := s.sendGameUpperSuccess(member.session, 51, ack); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
		if err := s.sendGameUpperRawClass(member.session, 504, buildCurrentPVPMatchingRoster(room), 0); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
		if err := s.sendGameUpperRawClass(member.session, 505, nil, 0); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
	}
	s.logGameEvent(a.member.session, "game-pvp-matching-paired", "peer", b.member.characterID, "token", room.matchingToken)
	err := s.ensureGameplayTimeQueue().ScheduleAfter(currentPVPMatchingTimerName(room), 45*time.Second, func(time.Time) {
		s.pvpMu.Lock()
		defer s.pvpMu.Unlock()
		if s.pvpRooms[id] == room && !room.started {
			s.closeCurrentPVPMatchingLocked(room, nil)
		}
	})
	if err != nil {
		s.closeCurrentPVPMatchingLocked(room, nil)
	}
	return err
}

func buildCurrentPVPMatchingRoster(room *currentPVPRoom) []byte {
	var w packetWriter
	if room.battleMode == 8 {
		w.writeByte(8)
	} else {
		w.writeByte(2)
	} // matchingmode.etc scene, distinct from C2S game type.
	w.writeUint32(room.matchingToken)
	w.writeByte(2)
	for seat, member := range room.members[:2] {
		w.writeUint16(member.characterID)
		w.writeByte(byte(seat + 1))
		w.writeUint32(member.rankPoint)
		// sub_1E1EB40 labels this badge "previous season" (dstr 36651).
		// No season history is stored; current grade is not that badge.
		w.writeByte(0)
		w.writeUint32(0) // special-character RP override, unused with flag=0
		w.writeByte(0)   // no special-character override
		if room.battleMode == 8 {
			writeCurrentPVPTotalSummary(&w, member.total)
		}
	}
	return w.bytes()
}

func (s *Service) completeCurrentPVPMatching(session *gameSession, body []byte) error {
	if len(body) != 4 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || room.matchingToken == 0 || room.matchingToken != binary.LittleEndian.Uint32(body) || room.started {
		return nil
	}
	seat := room.seatFor(session)
	if seat < 0 || room.matchingLoaded[seat] {
		return nil
	}
	room.matchingLoaded[seat] = true
	if !room.matchingLoaded[0] || !room.matchingLoaded[1] {
		return nil
	}
	s.ensureGameplayTimeQueue().Cancel(currentPVPMatchingTimerName(room))
	// sub_1E12820(2) advances state 3 -> 4; native op45 selects the PVF
	// map and sub_3446230 advances the matching loader to state 5.
	for _, member := range room.members[:2] {
		if err := s.sendGameUpperRawClass(member.session, 506, []byte{2}, 0); err != nil {
			s.closeCurrentPVPMatchingLocked(room, nil)
			return err
		}
	}
	if err := s.startCurrentPVPRoomLocked(room, room.members[0].session); err != nil {
		s.closeCurrentPVPMatchingLocked(room, nil)
		return err
	}
	if !room.started {
		s.closeCurrentPVPMatchingLocked(room, nil)
	}
	return nil
}

func (s *Service) closeCurrentPVPMatchingLocked(room *currentPVPRoom, disconnected *gameSession) {
	if room != nil && s.partyUDPRelay != nil {
		s.partyUDPRelay.closePVPRoom(room.id)
	}
	completed := room.match != nil && room.match.resultSent
	s.ensureGameplayTimeQueue().Cancel(currentPVPMatchingTimerName(room))
	if room.match != nil {
		s.ensureGameplayTimeQueue().Cancel(currentPVPResultTimerName(room, room.match))
		if room.match.total != nil {
			s.ensureGameplayTimeQueue().Cancel(currentPVPTotalPhaseTimerName(room, room.match))
		}
	}
	delete(s.pvpRooms, room.id)
	room.started, room.match = false, nil
	for seat, member := range room.members {
		if member == nil {
			continue
		}
		room.members[seat] = nil
		member.session.pvpRoom = nil
		if member.session == disconnected {
			continue
		}
		// Leave the map while the native room still exists, then clear its
		// local seat before freeing it (same order as manual-room departure).
		if err := s.returnCurrentPVPMemberToTown(member); err != nil {
			s.logGameEvent(member.session, "game-pvp-matching-return-failed", "error", err)
		}
		for _, packet := range []struct {
			id   uint16
			body []byte
		}{
			{506, []byte{1}},
			{43, buildCurrentPVPSeatStateBody(room.id, room.battleMode, byte(seat), 0xff, 0xffff)},
			{42, buildCurrentPVPDestroyedRoomStateBody(room.id)},
		} {
			// Native op506=1 opens dstr36390 (matching failed). A settled
			// match already completed; return and clear its seats normally.
			if completed && packet.id == 506 {
				continue
			}
			if err := s.sendGameUpperRawClass(member.session, packet.id, packet.body, 0); err != nil {
				s.logGameEvent(member.session, "game-pvp-matching-close-write-failed", "error", err)
			}
		}
	}
}

func currentPVPMatchingGameType(session *gameSession) uint16 {
	if isCurrentPVPTotalChannel(session.channel) {
		return 7
	}
	return 0
}
