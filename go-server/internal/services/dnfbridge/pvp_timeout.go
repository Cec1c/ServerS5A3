package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func currentPVPMinimumMatchDuration(source initialEquipmentTextSource, mode, channelType byte) (time.Duration, error) {
	text, err := source.ReadText("etc/pvp_ref.etc")
	if err != nil {
		return 0, err
	}
	doc, err := dnfpvf.Parse("etc/pvp_ref.etc", text)
	if err != nil {
		return 0, err
	}
	key := "pvp normal battle time"
	if mode == 3 || mode == 5 || mode == 8 {
		key = "pvp relay battle time"
	}
	// 33FC760 -> 2264960 -> 2263F40 includes preliminary tournaments (31/41).
	// vfunc30 (2738000) selects the relay column for battle mode8.
	switch channelType {
	case 8, 13, 20, 24, 31, 41:
		key += " for tournament"
	}
	values := doc.Ints(key)
	if len(values) != 1 || values[0] <= 0 || values[0] > 3600000 {
		return 0, fmt.Errorf("invalid PVF PVP duration for %s", key)
	}
	return time.Duration(values[0]) * time.Millisecond, nil
}

func (s *Service) handleCurrentPVPTimeout(session *gameSession, body []byte) error {
	if session == nil || !isPVPChannel(session.channel) || len(body) != 4*currentPVPRoomSeatCount {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || s.pvpRooms[room.id] != room || room.channelID != session.channel.ID ||
		!room.started || room.match == nil || room.match.resultSent || room.match.settlementPending || !room.combatant(room.seatFor(session)) {
		return nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	minimum, err := currentPVPMinimumMatchDuration(archive, room.battleMode, session.channel.Type)
	if err != nil {
		return err
	}
	startedAt := room.match.startedAt
	if m := room.match.total; m != nil {
		if m.choosing {
			return nil
		}
		startedAt = m.roundStarted
	}
	if startedAt.IsZero() || s.gameplayNow().Sub(startedAt) < minimum {
		return nil
	}
	var scores [currentPVPRoomSeatCount]uint32
	reporter := -1
	for seat := range scores {
		scores[seat] = binary.LittleEndian.Uint32(body[4*seat:])
		if scores[seat] > 0x7fffffff || (!room.combatant(seat) && scores[seat] != 0) {
			return nil
		}
		if room.combatant(seat) && (reporter < 0 || scores[seat] >= scores[reporter]) {
			reporter = seat
		}
	}
	// sub_3446540 elects the last highest-damage seat to report all eight
	// cumulative damage percentages. The report never awards durable rewards.
	if room.seatFor(session) != reporter {
		return nil
	}
	winner := currentPVPTimeoutWinner(room, scores)
	s.logGameEvent(session, "game-pvp-timeout-accepted", "room_id", room.id, "scores", scores, "winner_seat", winner)
	if room.match.total != nil {
		return s.finishCurrentPVPTotalTimeoutLocked(room, session, winner)
	}
	return s.sendCurrentPVPResultLocked(room, session, winner)
}

func currentPVPTimeoutWinner(room *currentPVPRoom, scores [currentPVPRoomSeatCount]uint32) byte {
	var totals [currentPVPRoomSeatCount]uint64
	var counts [currentPVPRoomSeatCount]uint64
	var representatives [currentPVPRoomSeatCount]byte
	for seat := range room.members {
		if !room.combatant(seat) {
			continue
		}
		side := seat
		if room.battleMode != 1 && room.battleMode != 4 {
			side = int(room.seatState[seat])
		}
		if counts[side] == 0 {
			representatives[side] = byte(seat)
		}
		counts[side]++
		totals[side] += uint64(scores[seat])
	}
	best, tied := -1, false
	for side := range counts {
		if counts[side] == 0 {
			continue
		}
		if best < 0 || totals[side]*counts[best] < totals[best]*counts[side] {
			best, tied = side, false
		} else if totals[side]*counts[best] == totals[best]*counts[side] {
			tied = true
		}
	}
	if best < 0 || tied {
		return 0xff
	}
	return representatives[best]
}
