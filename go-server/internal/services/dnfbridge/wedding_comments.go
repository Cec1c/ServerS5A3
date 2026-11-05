package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type currentWeddingCommentRequest struct {
	Key, ID uint32
	Mode    byte
	Text    string
}

func decodeCurrentWeddingCommentRequest(op uint16, body []byte) (currentWeddingCommentRequest, bool) {
	var q currentWeddingCommentRequest
	switch op {
	case 1040:
		if len(body) < 8 {
			return q, false
		}
		// Native1040 uses scalar key + byte-length string, not the raw
		// structures (with their internal prefix) used by 1041/1042.
		q.Key = binary.LittleEndian.Uint32(body)
		text, ok := parseCurrentGuildText(body[4:], 255)
		q.Text = text
		return q, ok && q.Key != 0 && len(text) > 0
	case 1041:
		if len(body) != 22 {
			return q, false
		}
		q.Key, q.ID, q.Mode = binary.LittleEndian.Uint32(body[13:]), binary.LittleEndian.Uint32(body[17:]), body[21]
		return q, q.Key != 0 && q.ID != 0 && q.Mode <= 1
	case 1042:
		if len(body) != 18 {
			return q, false
		}
		q.Key, q.Mode = binary.LittleEndian.Uint32(body[13:]), body[17]
		return q, q.Key != 0 && q.Mode <= 1
	}
	return q, false
}

func buildCurrentWeddingCommentACK(flag byte, result int32, id uint32, op uint16) []byte {
	body := []byte{flag}
	body = binary.LittleEndian.AppendUint32(body, uint32(result))
	if op != 1040 {
		body = binary.LittleEndian.AppendUint32(body, id)
	}
	if op == 1042 {
		body = binary.LittleEndian.AppendUint32(body, 0)
	}
	return body
}

func buildCurrentWeddingCommentsACK(key uint32, rows []wedding.GuestComment) ([]byte, bool) {
	if len(rows) > 200 {
		return nil, false
	}
	body := buildCurrentWeddingCommentACK(1, 0, key, 1042)
	binary.LittleEndian.PutUint32(body[9:], uint32(len(rows)))
	for _, row := range rows {
		text, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(row.Text))
		if err != nil || len(text) > 255 || row.ID == 0 {
			return nil, false
		}
		record := make([]byte, 42)
		binary.LittleEndian.PutUint32(record, row.ID)
		copy(record[4:33], rosterNameBytes(row.Name))
		binary.LittleEndian.PutUint32(record[34:], row.UpdatedAt)
		binary.LittleEndian.PutUint32(record[38:], uint32(len(text)))
		body = append(body, record...)
		body = append(body, text...)
	}
	return body, true
}

func (s *Service) currentWeddingCommentAuthorization(ctx context.Context, session *gameSession, key uint32) (wedding.CommentRequest, bool) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return wedding.CommentRequest{}, false
	}
	c, err := s.currentWeddingCharacter(ctx, identity)
	if err != nil {
		return wedding.CommentRequest{}, false
	}
	req := wedding.CommentRequest{Actor: wedding.Actor{AccountID: c.AccountID, CharacterID: c.CharacterID}, Key: key, Now: s.gameplayNow()}
	state, err := wedding.Read(c)
	if err == nil && state.PartnerID != 0 && state.RelationToken == int64(key) {
		req.Revalidate = func() bool {
			return s.boundGameSessionCharacterCurrent(identity) && s.accountIDForSession(session) == c.AccountID
		}
		return req, true
	}
	ledger, handled, err := s.currentWeddingGuestLoadingProof(ctx, session)
	if err != nil || !handled || ledger.Key != key || s.onlinePlayers == nil {
		return req, false
	}
	p, present := s.onlinePlayers.PlayerForCharacter(identity.character)
	if !present || p.Session != session || p.presenceGeneration == 0 {
		return req, false
	}
	presenceGeneration := p.presenceGeneration
	instance := p.Instance
	req.Revalidate = func() bool {
		if !s.boundGameSessionCharacterCurrent(identity) || s.accountIDForSession(session) != c.AccountID {
			return false
		}
		current, found := s.onlinePlayers.PlayerForCharacter(identity.character)
		if !found || current.Session != session || current.presenceGeneration != presenceGeneration || current.TownID != 39 || current.AreaID != ledger.Area || current.ChannelID != int(ledger.Channel) || current.Instance != instance {
			return false
		}
		return s.currentWeddingChurchTownSceneCurrent(identity, current, wedding.CeremonyRoute{Channel: ledger.Channel, Area: ledger.Area})
	}
	return req, req.Revalidate()
}

func (s *Service) handleCurrentWeddingComment(session *gameSession, body []byte, op uint16) error {
	q, valid := decodeCurrentWeddingCommentRequest(op, body)
	fail := func() error {
		id := q.ID
		if op == 1042 {
			id = q.Key
		}
		return s.sendGameUpperRawClass(session, op, buildCurrentWeddingCommentACK(0, -1, id, op), 1)
	}
	if !valid {
		return fail()
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	req, allowed := s.currentWeddingCommentAuthorization(ctx, session, q.Key)
	if !allowed {
		return fail()
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return fail()
	}
	switch op {
	case 1040:
		_, err = owner.UpdateGuestComment(ctx, req, q.Text)
	case 1041:
		err = owner.DeleteGuestComment(ctx, req, q.ID)
	case 1042:
		rows, loadErr := owner.LoadGuestComments(ctx, req)
		if loadErr != nil {
			return fail()
		}
		ack, valid := buildCurrentWeddingCommentsACK(q.Key, rows)
		if !valid {
			return fail()
		}
		return s.sendGameUpperRawClass(session, op, ack, 1)
	}
	if err != nil {
		return fail()
	}
	s.logInfo("dnfbridge wedding comment committed", "event", "game-wedding-comment-committed", "char_id", req.Actor.CharacterID, "wedding_key", q.Key, "operation", op, "row_id", strconv.FormatUint(uint64(q.ID), 10))
	return s.sendGameUpperRawClass(session, op, buildCurrentWeddingCommentACK(1, 0, q.ID, op), 1)
}

func (s *Service) handleCurrentWeddingUpdateGuestComment(p *gameSession, b []byte) error {
	return s.handleCurrentWeddingComment(p, b, 1040)
}
func (s *Service) handleCurrentWeddingDeleteGuestComment(p *gameSession, b []byte) error {
	return s.handleCurrentWeddingComment(p, b, 1041)
}
func (s *Service) handleCurrentWeddingLoadGuestComment(p *gameSession, b []byte) error {
	return s.handleCurrentWeddingComment(p, b, 1042)
}
