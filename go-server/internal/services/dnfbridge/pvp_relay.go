package dnfbridge

import (
	"fmt"
	"math/rand/v2"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Mode 3 is the native relay arena. Seats remain attached to their owners;
// class-0/112 selects one living fighter from each team.
type currentPVPRelayMatch struct {
	loaded    [currentPVPRoomSeatCount]bool
	selected  [2]byte
	requested [currentPVPRoomSeatCount]bool
	choosing  bool
	pending   bool
	phase     uint64
	selection []byte
	// Current native clients start relay selection with C2S112 and do not
	// send C2S298. Their room notifications cannot be gated on that opcode.
	requestDriven bool
}

func newCurrentPVPRelayMatch() *currentPVPRelayMatch {
	return &currentPVPRelayMatch{selected: [2]byte{255, 255}, choosing: true}
}

func (room *currentPVPRoom) relayFighter(seat int) bool {
	m := room.match.relay
	return room.combatant(seat) && !room.match.dead[seat] && !m.choosing &&
		(m.selected[0] == byte(seat) || m.selected[1] == byte(seat))
}

func currentPVPRelayTimerName(room *currentPVPRoom, match *currentPVPMatch) string {
	return fmt.Sprintf("dnf-pvp-relay:%d:%p", room.id, match)
}

func (s *Service) handleCurrentPVPRelayLoadLocked(room *currentPVPRoom, session *gameSession, opcode uint16) error {
	if opcode != uint16(dnfenum.CmdPacketCompleteLoadPVP) || room.match == nil || room.match.relay == nil || room.match.settlementPending {
		return nil
	}
	seat := room.seatFor(session)
	m := room.match.relay
	if seat < 0 || m.loaded[seat] {
		return nil
	}
	m.loaded[seat] = true
	if !room.combatant(seat) {
		if len(m.selection) > 0 {
			return s.sendGameUpperRawClass(session, 112, m.selection, 0)
		}
		return nil
	}
	if !m.choosing || len(m.selection) > 0 {
		return nil // A delayed load must not restart an active selection phase.
	}
	for other := range room.members {
		if room.combatant(other) && !m.loaded[other] {
			return nil
		}
	}
	return s.scheduleCurrentPVPRelaySelectionLocked(room)
}

func (s *Service) scheduleCurrentPVPRelaySelectionLocked(room *currentPVPRoom) error {
	match := room.match
	m := match.relay
	if m.pending {
		return nil
	}
	m.phase++
	phase := m.phase
	// Native state 8 presents a five-second relay selection countdown.
	err := s.ensureGameplayTimeQueue().ScheduleAfter(currentPVPRelayTimerName(room, match), 5*time.Second, func(time.Time) {
		s.pvpMu.Lock()
		defer s.pvpMu.Unlock()
		if s.pvpRooms[room.id] != room || room.match != match || !room.started || match.settlementPending || m.phase != phase {
			return
		}
		m.pending = false
		s.rollCurrentPVPRelayLocked(room)
	})
	if err == nil {
		m.pending = true
	}
	return err
}

func (s *Service) rollCurrentPVPRelayLocked(room *currentPVPRoom) {
	m := room.match.relay
	var candidates [2][]byte
	for side, selected := range m.selected {
		if selected == 255 || room.match.dead[selected] {
			for seat := range room.members {
				if room.combatant(seat) && !room.match.dead[seat] && room.seatState[seat] == byte(side+1) {
					candidates[side] = append(candidates[side], byte(seat))
				}
			}
		} else {
			for seat, requested := range m.requested {
				if requested && room.combatant(seat) && !room.match.dead[seat] && room.seatState[seat] == byte(side+1) {
					candidates[side] = append(candidates[side], byte(seat))
				}
			}
		}
	}
	if len(candidates[0])+len(candidates[1]) == 0 {
		return // All substitution requests were withdrawn.
	}
	var w packetWriter
	w.writeByte(2)
	for side, seats := range candidates {
		if len(seats) == 0 {
			// Native vfunc108(-1) retains the survivor and records its seat for
			// the relay HP/MP carry-over logic.
			w.writeByte(255)
			w.writeByte(0)
			continue
		}
		// Scores are server-owned rolls from 1 through 100. Sample without
		// replacement so a tied maximum cannot leave selection ambiguous.
		scores := rand.Perm(100)[:len(seats)]
		best := 0
		for i := range seats {
			if scores[i] > scores[best] {
				best = i
			}
		}
		m.selected[side] = seats[best]
		w.writeByte(seats[best])
		w.writeByte(byte(len(seats)))
		for i, seat := range seats {
			w.writeByte(seat)
			w.writeUint16(uint16(scores[i] + 1))
		}
	}
	m.requested = [currentPVPRoomSeatCount]bool{}
	m.choosing = false
	m.selection = append([]byte(nil), w.bytes()...)
	// A late observer has no previous selection for the -1 retain sentinel.
	for side, offset := 0, 1; side < 2; side++ {
		m.selection[offset] = m.selected[side]
		offset += 2 + 3*int(m.selection[offset+1])
	}
	for seat, member := range room.members {
		if member != nil && (m.loaded[seat] || m.requestDriven) {
			if err := s.sendGameUpperRawClass(member.session, 112, w.bytes(), 0); err != nil {
				s.logGameEvent(member.session, "game-pvp-relay-selection-write-failed", "error", err)
			}
		}
	}
	s.logGameEvent(room.members[room.manager].session, "game-pvp-relay-selected", "room_id", room.id, "selected", m.selected)
}

func (s *Service) handleCurrentPVPRelayRequestFight(session *gameSession, body []byte) error {
	// Native 36F4BA0 writes C2S112 with no body, including during initial
	// selection. It is distinct from the class-0/112 dice-result notification.
	if session == nil || !isPVPChannel(session.channel) || len(body) != 0 {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || s.pvpRooms[room.id] != room || room.channelID != session.channel.ID || !room.started || room.match == nil || room.match.relay == nil || room.match.settlementPending {
		return nil
	}
	seat := room.seatFor(session)
	m := room.match.relay
	if !room.combatant(seat) || room.match.dead[seat] {
		return nil
	}
	initial := m.choosing && len(m.selection) == 0
	if initial {
		// The real relay trace has op45 -> C2S112 on both clients, with no
		// load packets. Start the shared selection window on the first
		// request; teammates need not all click to become dice candidates.
		m.requestDriven = true
	} else if (!m.loaded[seat] && !m.requestDriven) || m.choosing || room.relayFighter(seat) {
		return nil
	}
	m.requested[seat] = !m.requested[seat]
	// Native class-0/113 toggles this seat's request indicator and starts
	// the local request countdown. Notify the whole room, including observers.
	for _, member := range room.members {
		if member != nil {
			if err := s.sendGameUpperRawClass(member.session, 113, []byte{byte(seat)}, 0); err != nil {
				s.logGameEvent(member.session, "game-pvp-relay-request-write-failed", "error", err)
			}
		}
	}
	s.logGameEvent(session, "game-pvp-relay-request", "room_id", room.id,
		"char_id", session.selectedCharacterID, "seat", seat, "initial", initial,
		"requested", m.requested[seat], "selection_pending", m.pending)
	return s.scheduleCurrentPVPRelaySelectionLocked(room)
}

func (s *Service) finishCurrentPVPRelayRoundLocked(room *currentPVPRoom) error {
	m := room.match.relay
	s.ensureGameplayTimeQueue().Cancel(currentPVPRelayTimerName(room, room.match))
	m.phase++
	m.pending = false
	m.choosing = true
	m.requested = [currentPVPRoomSeatCount]bool{}
	return s.scheduleCurrentPVPRelaySelectionLocked(room)
}
