package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
)

type currentPVPMatch struct {
	total             *currentPVPTotalMatch
	dead              [currentPVPRoomSeatCount]bool
	kills             [currentPVPRoomSeatCount]uint32
	ended             [currentPVPRoomSeatCount]bool
	winner            byte
	resultSent        bool
	settlementPending bool
	startedAt         time.Time
	completedAt       time.Time
	settled           [currentPVPRoomSeatCount]*dnfpvp.Snapshot
	resultStage       [currentPVPRoomSeatCount]byte
	missionReports    [currentPVPRoomSeatCount]map[uint16]bool
	poseReports       [currentPVPRoomSeatCount]map[uint16]byte
	questPublished    bool
}

func (room *currentPVPRoom) combatant(seat int) bool {
	return seat >= 0 && seat < currentPVPRoomSeatCount && room.members[seat] != nil &&
		room.seatState[seat] <= 2
}

func (room *currentPVPRoom) survivingPVPWinner() (byte, bool) {
	winner := byte(0xff)
	for seat := range room.members {
		if !room.combatant(seat) || room.match.dead[seat] {
			continue
		}
		if winner == 0xff {
			winner = byte(seat)
			continue
		}
		if room.battleMode == 1 || room.battleMode == 4 || room.seatState[seat] != room.seatState[winner] {
			return 0xff, false
		}
	}
	return winner, true
}

func (s *Service) handleCurrentPVPDeath(session *gameSession, body []byte) error {
	// sub_24A7470 emits victim/killer actor keys, perfect-kill flag and HP%.
	if session == nil || !isPVPChannel(session.channel) || len(body) != 7 || body[4] > 1 ||
		binary.LittleEndian.Uint16(body[5:]) > 100 || binary.LittleEndian.Uint16(body) != session.selectedCharacterID {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || !room.started || room.match == nil || room.match.resultSent {
		return nil
	}
	if room.match.settlementPending {
		return nil
	}
	seat := room.seatFor(session)
	if !room.combatant(seat) || room.match.dead[seat] {
		return nil
	}
	if m := room.match.total; m != nil && (m.choosing || m.selected[seat] >= 3) {
		return nil
	}
	killerID := binary.LittleEndian.Uint16(body[2:])
	killer := byte(0xff)
	if killerID != 0xffff {
		for other, member := range room.members {
			if room.combatant(other) && other != seat && member.characterID == killerID &&
				(room.battleMode == 1 || room.battleMode == 4 || room.seatState[other] != room.seatState[seat]) {
				killer = byte(other)
				break
			}
		}
		if killer == 0xff {
			return nil
		}
	}
	room.match.dead[seat] = true
	if killer != 0xff {
		room.match.kills[killer]++
		if m := room.match.total; m != nil && m.selected[killer] < 3 {
			m.kills[killer][m.selected[killer]]++
		}
	}
	for _, member := range room.members {
		if member != nil {
			if err := s.sendGameUpperRawClass(member.session, 46, []byte{byte(seat), killer}, 0); err != nil {
				s.logGameEvent(member.session, "game-pvp-death-write-failed", "error", err)
			}
		}
	}
	if room.match.total != nil {
		return s.finishCurrentPVPTotalRoundLocked(room, session, seat)
	}
	winner, terminal := room.survivingPVPWinner()
	s.logGameEvent(session, "game-pvp-death-accepted", "room_id", room.id, "seat", seat, "killer", killer, "terminal", terminal)
	if !terminal {
		return nil
	}
	return s.sendCurrentPVPResultLocked(room, session, winner)
}

func (s *Service) sendCurrentPVPResultLocked(room *currentPVPRoom, session *gameSession, winner byte) error {
	if room.match == nil || room.match.resultSent {
		return nil
	}
	if !room.match.settlementPending {
		if room.match.total != nil {
			s.ensureGameplayTimeQueue().Cancel(currentPVPTotalPhaseTimerName(room, room.match))
		}
		room.match.winner = winner
		room.match.settlementPending = true
		room.match.completedAt = s.gameplayNow()
	}
	err := s.settleCurrentPVPDaily(room)
	if err == nil && !room.match.questPublished {
		err = s.settleCurrentPVPWinQuests(room)
		room.match.questPublished = err == nil
	}
	if err == nil {
		err = s.publishCurrentPVPResults(room)
	}
	if err != nil {
		s.logGameEvent(session, "game-pvp-quest-settlement-retry", "room_id", room.id, "error", err)
		match := room.match
		return s.ensureGameplayTimeQueue().ScheduleAfter(currentPVPResultTimerName(room, match), time.Second, func(time.Time) {
			s.pvpMu.Lock()
			defer s.pvpMu.Unlock()
			if s.pvpRooms[room.id] == room && room.match == match && room.started {
				if err := s.sendCurrentPVPResultLocked(room, session, match.winner); err != nil {
					s.logGameEvent(session, "game-pvp-quest-retry-schedule-failed", "error", err)
				}
			}
		})
	}
	room.match.resultSent = true
	match := room.match
	queue := s.ensureGameplayTimeQueue()
	if err := queue.ScheduleAfter(currentPVPResultTimerName(room, match), 30*time.Second, func(time.Time) {
		s.pvpMu.Lock()
		defer s.pvpMu.Unlock()
		if s.pvpRooms[room.id] == room && room.match == match && room.started {
			s.finishCurrentPVPResultLocked(room)
		}
	}); err != nil {
		return err
	}
	s.logGameEvent(session, "game-pvp-result-sent", "room_id", room.id, "winner_seat", winner)
	return nil
}

func (s *Service) settleCurrentPVPWinQuests(room *currentPVPRoom) error {
	// PVF daily_PVP_02/03 require a ranked personVsPerson win. Manual rooms,
	// practice, observers and draws do not satisfy that objective.
	if room.matchingToken == 0 || room.match.winner > 1 || room.battleMode != 2 ||
		!room.combatant(0) || !room.combatant(1) {
		return nil
	}
	member := room.members[room.match.winner]
	if member.session == nil || member.session.channel.Type != 8 {
		return nil
	}
	for seat := 2; seat < len(room.members); seat++ {
		if room.combatant(seat) {
			return nil
		}
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfquest.ErrOwnerUnavailable
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("pvp:%d:%d:%p", room.id, room.match.startedAt.UnixNano(), room.match)
	result, err := owner.ApplyPVPWin(ctx, catalog, member.session.accountID,
		strconv.FormatUint(uint64(member.characterID), 10), key, room.match.completedAt)
	if err != nil {
		return err
	}
	if result.SnapshotRequired {
		if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(member.session, result.Quest, "pvp_win"); err != nil {
			return err
		}
	}
	return nil
}

func buildCurrentPVPResultBody(room *currentPVPRoom, recipient *currentPVPMember) []byte {
	var w packetWriter
	w.writeByte(room.match.winner)
	// sub_3291EC0 replaces inventory currency slot 2, as does op37.
	w.writeUint32(recipient.currencySlot2)
	grade, experienceDelta := recipient.grade, int64(0)
	if seat := room.seatFor(recipient.session); seat >= 0 && room.match.settled[seat] != nil {
		state := room.match.settled[seat]
		grade = byte(state.Character.Stats[dnfpvp.GradeKey])
		experienceDelta = state.ExperienceDelta
	}
	if seat := room.seatFor(recipient.session); seat >= 0 && room.match.total != nil && room.match.total.settled[seat] != nil {
		state := room.match.total.settled[seat]
		for i, c := range state.Characters {
			if c.CharacterID == strconv.Itoa(int(recipient.characterID)) {
				grade = byte(c.Stats[dnfpvp.GradeKey])
				experienceDelta = c.Stats[dnfpvp.ExperienceKey] - state.Before[i].Stats[dnfpvp.ExperienceKey]
			}
		}
	}
	w.writeByte(grade)
	// sub_1D681E0 -> sub_3291F60 -> sub_32949A0 draws this signed gain.
	w.writeInt32(int(experienceDelta))
	count := 0
	for seat := range room.members {
		if room.combatant(seat) {
			count++
		}
	}
	w.writeByte(byte(count))
	for seat, member := range room.members {
		if !room.combatant(seat) {
			continue
		}
		// sub_1D681E0 consumes exactly 23 bytes per current-client row.
		w.writeUint16(member.characterID)
		w.writeUint32(room.match.kills[seat])
		grade := member.grade
		if state := room.match.settled[seat]; state != nil {
			grade = byte(state.Character.Stats[dnfpvp.GradeKey])
		}
		w.writeByte(grade)
		w.writeUint32(0)
		beforeRP, afterRP := member.rankPoint, member.rankPoint
		if room.match.total != nil && member.total != nil {
			beforeRP, afterRP = member.total.rankPoint, member.total.rankPoint
			if state := room.match.total.settled[seat]; state != nil {
				afterRP = uint32(state.Stats[dnfpvp.RankPointKey])
			}
		}
		w.writeUint32(beforeRP)
		w.writeUint32(afterRP)
		w.writeUint32(room.match.kills[seat])
	}
	w.writeUint16(0)      // no ace bonus
	w.writeUint16(0xffff) // no all-kill bonus
	w.writeUint32(0)
	if room.match.winner == 0xff {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
	w.writeByte(0xff)
	w.writeUint32(0)
	w.writeByte(0)
	return w.bytes()
}

func currentPVPResultTimerName(room *currentPVPRoom, match *currentPVPMatch) string {
	return fmt.Sprintf("dnf-pvp-result:%d:%p", room.id, match)
}

func (s *Service) handleCurrentPVPResultEnd(session *gameSession, body []byte) error {
	if session == nil || !isPVPChannel(session.channel) || len(body) != 0 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || !room.started || room.match == nil || !room.match.resultSent {
		return nil
	}
	seat := room.seatFor(session)
	if seat < 0 {
		return nil
	}
	room.match.ended[seat] = true
	for seat, member := range room.members {
		if member != nil && !room.match.ended[seat] {
			return nil
		}
	}
	s.ensureGameplayTimeQueue().Cancel(currentPVPResultTimerName(room, room.match))
	s.finishCurrentPVPResultLocked(room)
	return nil
}

func (s *Service) finishCurrentPVPResultLocked(room *currentPVPRoom) {
	if room.matchingToken != 0 {
		s.closeCurrentPVPMatchingLocked(room, nil)
		return
	}
	room.started = false
	room.match = nil
	room.ready = [currentPVPRoomSeatCount]byte{}
	for _, member := range room.members {
		if member == nil {
			continue
		}
		for seat, actor := range room.members {
			if actor != nil {
				if err := s.sendGameUpperRawClass(member.session, 44, []byte{byte(seat), 0}, 0); err != nil {
					s.logGameEvent(member.session, "game-pvp-ready-reset-write-failed", "error", err)
				}
			}
		}
	}
	s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPRoomStateMsgID,
		buildCurrentPVPRoomStateBody(room.id, room.state(), room.manager, room.request.mapIndex, room.battleMode, 0))
}
