package dnfbridge

import (
	"errors"
	"time"
)

// The matching native recipient dialog starts a 6000ms timer. Its state2
// timeout closes locally without sending CMD1033, so the server owns this
// deadline too. Start it after successful request delivery, like the dialog.
const currentWeddingReservationReplyTimeout = 6 * time.Second

// Caller owns wedding.mu. Native 998 closes the corresponding request dialogs
// and displays the original timeout text for answer2/op2 on both endpoints.
func (s *Service) expireCurrentWeddingReservationInvitationLocked(inv *currentWeddingInvitation) error {
	if inv == nil || inv.request.Operation != 2 || s.wedding.invitations[inv.to.character] != inv || s.gameplayNow().Before(inv.expires) {
		return nil
	}
	delete(s.wedding.invitations, inv.to.character)
	if inv.timer != nil {
		inv.timer.Stop()
		inv.timer = nil
	}
	if s.wedding.reservationTimeouts == nil {
		s.wedding.reservationTimeouts = map[uint16]*currentWeddingInvitation{}
	}
	for id, old := range s.wedding.reservationTimeouts {
		if !s.gameplayNow().Before(old.expires.Add(time.Minute)) || !s.boundGameSessionCharacterCurrent(old.to) {
			delete(s.wedding.reservationTimeouts, id)
		}
	}
	s.wedding.reservationTimeouts[inv.to.character] = inv
	var published error
	for _, side := range []struct {
		who    boundGameSessionCharacter
		target uint16
	}{{inv.from, inv.to.character}, {inv.to, inv.from.character}} {
		if !s.boundGameSessionCharacterCurrent(side.who) {
			continue
		}
		response := currentWeddingResponse{currentWeddingRequest: inv.request, Answer: 2}
		response.Target = uint32(side.target)
		published = errors.Join(published, s.sendGameUpperRawClass(side.who.session, currentWeddingResponseNotice, buildCurrentWeddingResponseRecord(response, 0, false), 0))
	}
	s.logInfo("dnfbridge wedding invitation timeout", "event", "game-wedding-invitation-expired", "operation", 2, "from_char_id", inv.from.character, "to_char_id", inv.to.character, "timeout_ms", currentWeddingReservationReplyTimeout.Milliseconds(), "deadline_unix_ms", inv.expires.UnixMilli(), "notification_sent", published == nil)
	return published
}

func (s *Service) armCurrentWeddingReservationInvitationLocked(inv *currentWeddingInvitation) {
	if s.wedding.ceremoniesStopped || s.wedding.invitations[inv.to.character] != inv {
		return
	}
	if inv.timer != nil {
		inv.timer.Stop()
	}
	inv.timer = time.AfterFunc(max(inv.expires.Sub(s.gameplayNow()), 0), func() {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		if s.wedding.ceremoniesStopped || s.wedding.invitations[inv.to.character] != inv {
			return
		}
		if s.gameplayNow().Before(inv.expires) {
			s.armCurrentWeddingReservationInvitationLocked(inv)
			return
		}
		_ = s.expireCurrentWeddingReservationInvitationLocked(inv)
	})
}

// In this client, CMD1032 has no daily-limit popup branch. Its silent flag0
// finishes dispatch without opening a waiting dialog; NOTI998/op2/result-11
// then opens native window726 with dstr72784. -11 on CMD1032 is silent, while
// -200 selects the unrelated maintenance message. Keep the limit notification
// on its actual result handler, without opening an invitation for the partner.
func (s *Service) sendCurrentWeddingReservationDailyLimit(session *gameSession, q currentWeddingRequest) error {
	body := buildCurrentWeddingRequestRecord(q, -1, true)
	body[0] = 0
	ack := s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, body, 1)
	result := currentWeddingResponse{currentWeddingRequest: q}
	s.logGameEvent(session, "game-wedding-reservation-daily-limit", "target", q.Target, "result", -11)
	return errors.Join(ack, s.sendGameUpperRawClass(session, currentWeddingResponseNotice, buildCurrentWeddingResponseRecord(result, -11, false), 0))
}

// Classification1/1032 flag0 is a native silent failure. Explain the actual
// precondition through the existing system-text channel instead of mapping
// every rejected request to the unrelated "character not found" result -1.
func (s *Service) sendCurrentWeddingRequestHint(session *gameSession, q currentWeddingRequest, text string) error {
	body := buildCurrentWeddingRequestRecord(q, -1, true)
	body[0] = 0
	return errors.Join(s.sendGameUpperRawClass(session, currentWeddingRequestOpcode, body, 1), s.sendCurrentWeddingSystemText(session, text))
}

func (s *Service) sendCurrentWeddingSystemText(session *gameSession, text string) error {
	var w packetWriter
	w.writeByte(0)
	w.writeUint16(0xffff)
	w.writeByte(0)
	w.writeRawDstr(rosterNameBytes(text))
	w.writeRawDstr(nil)
	return s.sendGameUpperRawClass(session, currentAreaChatNotifyOpcode, w.bytes(), 0)
}
