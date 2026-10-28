package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
)

// The native op10/op11 failure callbacks always consume the request mode.
// A two-byte generic failure leaves that read outside the packet body.
func (s *Service) sendCurrentItemTradeInvitationFailure(ctx context.Context, session *gameSession, op uint16, code byte) error {
	return s.sendCurrentItemTradeInvitationFrame(ctx, session, op, 1, []byte{0, code, 1})
}

func (s *Service) currentItemTradeInvitationPeers(first, second boundGameSessionCharacter) bool {
	return first.character != second.character && s.boundGameSessionCharacterCurrent(first) &&
		s.boundGameSessionCharacterCurrent(second) && !isPVPChannel(first.session.channel) &&
		!isPVPChannel(second.session.channel) && !s.partyChannelMismatch(first.session, second.session) &&
		s.onlinePlayers != nil && s.onlinePlayers.PeerInSameArea(first.character, second.character)
}

func (s *Service) handleCurrentItemTradeInvitation(session *gameSession, request party.RequestPeerRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	first, current := s.boundGameSessionCharacterSnapshot(session)
	if !current {
		return nil
	}
	second, online := s.onlineGameSessionCharacterSnapshot(request.TargetID)
	if !online || !s.currentItemTradeInvitationPeers(first, second) {
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 10, 19)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{first, second})
	if err != nil {
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 10, 19)
	}
	defer release()
	s.tradeMu.Lock()
	busy := s.itemTrades[first.character] != nil || s.itemTrades[second.character] != nil
	s.tradeMu.Unlock()
	if busy || !s.currentItemTradeInvitationPeers(first, second) {
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 10, 19)
	}
	gold, created, valid := s.currentItemTradeInvitationCharacter(ctx, first)
	if !valid {
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 10, 19)
	}
	// Native mode 1 displays available gold and the peer character's creation
	// time. Both values come from the repository, not the client's claim.
	request.Value0, request.Value2 = gold, created
	manager := s.runtimePartyManagerForService()
	if _, ok := manager.RecordOnlinePeerRequest(second.character, second.generation, first.character, first.generation, 1); !ok {
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 10, 19)
	}
	forget := func() {
		manager.ConsumePeerRequest(second.character, second.generation, first.character, first.generation, 1)
	}
	if err := s.sendCurrentItemTradeInvitationFrame(ctx, session, 10, 1, party.BuildRequestPeerAck()); err != nil {
		forget()
		return err
	}
	if err := s.sendCurrentItemTradeInvitationFrame(ctx, second.session, 7, 0, party.BuildRequestPeerNotice(first.character, request)); err != nil {
		forget()
		// Only the failed peer stream is closed. Tell the requester that the
		// already acknowledged invitation could not be delivered.
		return s.sendCurrentItemTradeInvitationRecovery(session, 10, 1, []byte{0, 19, 1})
	}
	return nil
}

func (s *Service) handleCurrentItemTradeInvitationResponse(session *gameSession, request party.ResponsePeerRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	recipient, current := s.boundGameSessionCharacterSnapshot(session)
	requester, online := s.onlineGameSessionCharacterSnapshot(request.TargetID)
	if !current || !online || recipient.character == requester.character {
		return nil
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{recipient, requester})
	if err != nil {
		return nil
	}
	defer release()
	manager := s.runtimePartyManagerForService()
	if _, ok := manager.ConsumePeerRequest(recipient.character, recipient.generation, requester.character, requester.generation, 1); !ok {
		return nil
	}
	if request.RejectReason == 133 {
		// The reject sender closes its own prompt. Neither side receives a
		// success ACK, which would open a new local trade window.
		_ = s.sendCurrentItemTradeInvitationFailure(ctx, requester.session, 10, 19)
		return nil
	}
	if !s.currentItemTradeInvitationPeers(recipient, requester) {
		_ = s.sendCurrentItemTradeInvitationFailure(ctx, requester.session, 10, 19)
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 11, 19)
	}
	gold, created, valid := s.currentItemTradeInvitationCharacter(ctx, recipient)
	if !valid {
		_ = s.sendCurrentItemTradeInvitationFailure(ctx, requester.session, 10, 19)
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 11, 19)
	}
	trade := s.beginOnlineItemTradeState(session, requester.session, true)
	if trade == nil {
		_ = s.sendCurrentItemTradeInvitationFailure(ctx, requester.session, 10, 19)
		return s.sendCurrentItemTradeInvitationFailure(ctx, session, 11, 19)
	}
	rollback := func() {
		s.clearOnlineItemTrade(trade)
		// No offers can enter an opening trade. Reset only the two windows
		// involved in this handshake, with the native empty cancel body.
		for _, peer := range []*gameSession{session, requester.session} {
			_ = s.sendCurrentItemTradeInvitationRecovery(peer, currentItemTradeCancelMsg, 0, buildOnlineItemTradeCancelBody(nil, 0))
		}
	}
	body := append([]byte{1}, party.BuildResponsePeerAckPayload(requester.character, 1)...)
	if err := s.sendCurrentItemTradeInvitationFrame(ctx, session, 11, 1, body); err != nil {
		rollback()
		return err
	}
	// Mode 1 consumes the common seven bytes, then a trade flag and u32
	// character creation time. The old common-only notification was truncated.
	notice := append(party.BuildResponsePeerNotice(recipient.character, 1, gold), 0)
	notice = binary.LittleEndian.AppendUint32(notice, created)
	if err := s.sendCurrentItemTradeInvitationFrame(ctx, requester.session, 8, 0, notice); err != nil {
		rollback()
		return nil
	}
	s.tradeMu.Lock()
	trade.opening = false
	s.tradeMu.Unlock()
	return nil
}

// A slow peer can exhaust the handshake budget. Recovery has a separate
// bounded write so the healthy peer still receives its failure/window reset.
func (s *Service) sendCurrentItemTradeInvitationRecovery(session *gameSession, op uint16, class byte, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	return s.sendCurrentItemTradeInvitationFrame(ctx, session, op, class, body)
}

func (s *Service) currentItemTradeInvitationCharacter(ctx context.Context, identity boundGameSessionCharacter) (uint32, uint32, bool) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return 0, 0, false
	}
	record, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || record.CharacterID != strconv.Itoa(int(identity.character)) ||
		strings.TrimSpace(record.AccountID) != strings.TrimSpace(identity.session.accountID) ||
		record.CreatedAt.IsZero() || !s.boundGameSessionCharacterCurrent(identity) {
		return 0, 0, false
	}
	created, gold := record.CreatedAt.Unix(), record.Stats["gold"]
	if created < 0 || created > math.MaxInt32 || gold < 0 || gold > math.MaxInt32 {
		return 0, 0, false
	}
	return uint32(gold), uint32(created), true
}

func (s *Service) sendCurrentItemTradeInvitationFrame(ctx context.Context, session *gameSession, op uint16, class byte, body []byte) error {
	if session == nil || session.conn == nil {
		return errors.New("trade invitation connection unavailable")
	}
	if err := lockCurrentCrossSceneProfile(ctx, &session.wireMu); err != nil {
		return err
	}
	defer session.wireMu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.DeadlineExceeded
	}
	if err := session.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = session.conn.SetWriteDeadline(time.Now()); close(finished) })
	defer func() {
		if !stop() {
			<-finished
		}
		_ = session.conn.SetWriteDeadline(time.Time{})
	}()
	if err := s.sendGameUpperRawClassCodecLocked(session, op, body, class, true); err != nil {
		_ = session.conn.Close()
		return err
	}
	return nil
}
