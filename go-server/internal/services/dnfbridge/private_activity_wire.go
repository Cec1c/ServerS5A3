package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/activeness"
)

// Private extension, not any historical AP opcode. The matching EXE installs
// this exact callback and request producer on the authenticated game socket.
const privateActivityOpcode uint16 = 0x7f01

type privateActivityRequest struct {
	Claim, Character uint16
	Day, Fingerprint int64
	Generation       uint64
	Nonce            uint32
}

func parsePrivateActivityRequest(b []byte) (privateActivityRequest, error) {
	q := privateActivityRequest{}
	if len(b) != 48 || string(b[:4]) != "APQ2" || binary.LittleEndian.Uint16(b[4:]) != 2 || binary.LittleEndian.Uint16(b[10:]) != 0 {
		return q, activeness.ErrState
	}
	for _, v := range b[36:] {
		if v != 0 {
			return q, activeness.ErrState
		}
	}
	q.Claim = binary.LittleEndian.Uint16(b[6:])
	q.Character = binary.LittleEndian.Uint16(b[8:])
	q.Day = int64(binary.LittleEndian.Uint32(b[12:]))
	q.Fingerprint = int64(binary.LittleEndian.Uint64(b[16:]))
	q.Generation = binary.LittleEndian.Uint64(b[24:])
	q.Nonce = binary.LittleEndian.Uint32(b[32:])
	if q.Claim > 3 || q.Nonce == 0 || q.Claim == 0 && (q.Character != 0 || q.Day != 0 || q.Fingerprint != 0 || q.Generation != 0) || q.Claim > 0 && (q.Character == 0 || q.Day <= 0 || q.Fingerprint <= 0 || q.Generation == 0) {
		return privateActivityRequest{}, activeness.ErrState
	}
	return q, nil
}
func encodePrivateActivity(s activeness.Snapshot, char uint16, generation uint64, nonce uint32, status uint16) []byte {
	b := make([]byte, 184)
	copy(b, "APS2")
	put16 := func(i int, v uint16) { binary.LittleEndian.PutUint16(b[i:], v) }
	put32 := func(i int, v uint32) { binary.LittleEndian.PutUint32(b[i:], v) }
	put16(4, 2)
	put16(6, uint16(len(b)))
	put16(8, status)
	put16(10, char)
	put16(12, s.Points)
	put16(14, s.Rewards[2].Required)
	b[16] = s.Claimed
	put32(20, uint32(s.Day))
	binary.LittleEndian.PutUint64(b[24:], uint64(s.Fingerprint))
	binary.LittleEndian.PutUint64(b[32:], generation)
	put32(40, nonce)
	if s.Replayed {
		put32(44, 1)
	}
	for i, t := range s.Tasks {
		o := 48 + 8*i
		put16(o, t.ID)
		put16(o+2, t.Target)
		put16(o+4, t.Points)
		put16(o+6, s.Progress[i])
	}
	for i, r := range s.Rewards {
		o := 112 + 24*i
		put16(o, r.ID)
		put16(o+2, r.Required)
		put32(o+4, r.HappyTokens)
		put32(o+8, r.PotionID)
		put32(o+12, r.PotionCount)
		put32(o+16, r.InvitationID)
		put32(o+20, r.InvitationCount)
	}
	return b
}
func (s *Service) handlePrivateActivity(session *gameSession, classification byte, body []byte) error {
	if session == nil || classification != 1 {
		return nil
	}
	q, err := parsePrivateActivityRequest(body)
	if err != nil {
		return nil
	}
	var state activeness.Snapshot
	if session == nil || session.selectedCharacterID == 0 || q.Claim > 0 && q.Character != session.selectedCharacterID {
		err = activeness.ErrOwner
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		state, err = s.settlePrivateActivity(ctx, session, q.Claim, q.Day, q.Fingerprint, q.Generation)
	}
	code := uint16(0)
	if err != nil {
		code = 5
		switch {
		case errors.Is(err, activeness.ErrOwner):
			code = 1
		case errors.Is(err, activeness.ErrNotReady):
			code = 2
		case errors.Is(err, activeness.ErrStaleDay), errors.Is(err, activeness.ErrCatalogChanged):
			code = 3
		}
		s.logGameEvent(session, "private-activity-rejected", "stage", q.Claim, "error", err)
	}
	if err == nil && q.Claim > 0 {
		if err := s.sendCurrentAccountWallet(session); err != nil {
			return err
		}
		if err := s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "private_activity"); err != nil {
			return err
		}
	}
	return s.sendGameUpperRawClass(session, privateActivityOpcode, encodePrivateActivity(state, session.selectedCharacterID, session.characterGeneration, q.Nonce, code), 0)
}
