package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	"time"
)

// Each owner keeps one network seat. The selected actor index changes inside
// that owner's native total-match state; account/session identity never changes.
type currentPVPTotalMatch struct {
	loaded       [currentPVPRoomSeatCount]bool
	selected     [currentPVPRoomSeatCount]byte
	defeated     [currentPVPRoomSeatCount][3]bool
	choosing     bool
	roundStarted time.Time
	kills        [currentPVPRoomSeatCount][3]uint32
	reports      [currentPVPRoomSeatCount]map[uint16]uint32
	settled      [currentPVPRoomSeatCount]*dnfpvp.TotalSnapshot
	phase        uint64
	orderSent    [currentPVPRoomSeatCount]bool
}

func (room *currentPVPRoom) totalCombatants() []int {
	var seats []int
	for seat := range room.members {
		if room.combatant(seat) {
			seats = append(seats, seat)
		}
	}
	return seats
}

func newCurrentPVPTotalMatch() *currentPVPTotalMatch {
	m := &currentPVPTotalMatch{}
	for i := range m.selected {
		m.selected[i] = 255
	}
	return m
}

func (s *Service) openCurrentPVPTotalSelectionLocked(room *currentPVPRoom) error {
	m := room.match.total
	m.choosing = true
	m.orderSent = [currentPVPRoomSeatCount]bool{}
	for _, member := range room.members {
		if member != nil {
			if err := s.sendGameUpperRawClass(member.session, 804, nil, 0); err != nil {
				return err
			}
		}
	}
	return s.scheduleCurrentPVPTotalPhaseLocked(room, true)
}

func (s *Service) handleCurrentPVPTotalLoadLocked(room *currentPVPRoom, session *gameSession, opcode uint16) error {
	if room.match == nil || room.match.total == nil || opcode != uint16(dnfenum.CmdPacketCompleteLoadPVP) {
		return nil
	}
	seat := room.seatFor(session)
	m := room.match.total
	if !room.combatant(seat) || m.loaded[seat] {
		return nil
	}
	m.loaded[seat] = true
	for _, seat := range room.totalCombatants() {
		if !m.loaded[seat] {
			return nil
		}
	}
	return s.openCurrentPVPTotalSelectionLocked(room)
}

func (s *Service) handleCurrentPVPTotalOrder(session *gameSession, body []byte) error {
	if session == nil || len(body) != 1 || body[0] >= 3 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || s.pvpRooms[room.id] != room || !room.started || room.match == nil || room.match.total == nil || room.match.settlementPending {
		return nil
	}
	seat := room.seatFor(session)
	m := room.match.total
	if !room.combatant(seat) || !m.choosing || !m.loaded[seat] || m.defeated[seat][body[0]] {
		return nil
	}
	// Survivors retain their current actor. A second request in the same
	// selection phase cannot replace an already accepted choice.
	if m.selected[seat] != 255 {
		return nil
	}
	m.selected[seat] = body[0]
	seats := room.totalCombatants()
	if len(seats) != 2 {
		return nil
	}
	for _, n := range seats {
		if m.selected[n] == 255 {
			return nil
		}
	}
	return s.startCurrentPVPTotalRoundLocked(room)
}

func (s *Service) startCurrentPVPTotalRoundLocked(room *currentPVPRoom) error {
	m := room.match.total
	seats := room.totalCombatants()
	if len(seats) != 2 {
		return fmt.Errorf("total-match requires two owners")
	}
	var w packetWriter
	for _, n := range seats {
		w.writeUint32(uint32(room.members[n].characterID))
		w.writeByte(m.selected[n])
	}
	for seat, member := range room.members {
		if member != nil && !m.orderSent[seat] {
			if err := s.sendGameUpperRawClass(member.session, 803, w.bytes(), 0); err != nil {
				return err
			}
			m.orderSent[seat] = true
		}
	}
	m.choosing = false
	m.phase++
	m.roundStarted = s.gameplayNow()
	room.match.missionReports = [currentPVPRoomSeatCount]map[uint16]bool{}
	s.ensureGameplayTimeQueue().Cancel(currentPVPTotalPhaseTimerName(room, room.match))
	s.logGameEvent(room.members[seats[0]].session, "game-pvp-total-round-started", "room_id", room.id, "selected", m.selected)
	return nil
}

func currentPVPTotalPhaseTimerName(room *currentPVPRoom, match *currentPVPMatch) string {
	return fmt.Sprintf("dnf-pvp-total-phase:%d:%p", room.id, match)
}

func (s *Service) scheduleCurrentPVPTotalPhaseLocked(room *currentPVPRoom, selecting bool) error {
	match := room.match
	match.total.phase++
	phase := match.total.phase
	// Native state 15 gives five seconds for selection (3404470). This server
	// watchdog allows loading/network slack; the native client selects normally.
	return s.ensureGameplayTimeQueue().ScheduleAfter(currentPVPTotalPhaseTimerName(room, match), 45*time.Second, func(time.Time) {
		s.pvpMu.Lock()
		defer s.pvpMu.Unlock()
		if s.pvpRooms[room.id] != room || room.match != match || !room.started || match.settlementPending || match.total.phase != phase {
			return
		}
		if !selecting {
			if room.matchingToken != 0 {
				s.closeCurrentPVPMatchingLocked(room, nil)
			} else {
				for _, m := range room.members {
					if m != nil {
						_ = s.returnCurrentPVPMemberToTown(m)
					}
				}
				s.finishCurrentPVPResultLocked(room)
			}
			return
		}
		if !match.total.choosing {
			return
		}
		for _, seat := range room.totalCombatants() {
			if match.total.selected[seat] != 255 {
				continue
			}
			for i, dead := range match.total.defeated[seat] {
				if !dead {
					match.total.selected[seat] = byte(i)
					break
				}
			}
			if match.total.selected[seat] == 255 {
				return
			}
		}
		if err := s.startCurrentPVPTotalRoundLocked(room); err != nil {
			s.logGameEvent(room.members[room.manager].session, "game-pvp-total-watchdog-failed", "error", err)
		}
	})
}

func (s *Service) finishCurrentPVPTotalTimeoutLocked(room *currentPVPRoom, session *gameSession, winner byte) error {
	m := room.match.total
	if m.choosing {
		return nil
	}
	for _, seat := range room.totalCombatants() {
		if byte(seat) == winner {
			continue
		}
		index := m.selected[seat]
		if index >= 3 {
			return nil
		}
		m.defeated[seat][index] = true
		m.selected[seat] = 255
		room.match.dead[seat] = m.defeated[seat] == [3]bool{true, true, true}
		for _, member := range room.members {
			if member != nil {
				if err := s.sendGameUpperRawClass(member.session, 46, []byte{byte(seat), 255}, 0); err != nil {
					return err
				}
			}
		}
	}
	if win, done := room.survivingPVPWinner(); done {
		return s.sendCurrentPVPResultLocked(room, session, win)
	}
	return s.openCurrentPVPTotalSelectionLocked(room)
}

// A defeated character is exhausted, not the entire three-character team.
func (s *Service) finishCurrentPVPTotalRoundLocked(room *currentPVPRoom, session *gameSession, seat int) error {
	m := room.match.total
	index := m.selected[seat]
	if index >= 3 || m.choosing || m.defeated[seat][index] {
		return nil
	}
	m.defeated[seat][index] = true
	if m.defeated[seat] == [3]bool{true, true, true} {
		winner, terminal := room.survivingPVPWinner()
		if terminal {
			return s.sendCurrentPVPResultLocked(room, session, winner)
		}
		return nil
	}
	room.match.dead[seat] = false
	m.selected[seat] = 255
	return s.openCurrentPVPTotalSelectionLocked(room)
}
