package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Preserve spacing and notice line breaks. Name parsing trims them and also
// accepts a NUL terminator, which is inappropriate for an editable text field.
func parseCurrentGuildText(body []byte, limit int) (string, bool) {
	if len(body) < 4 || len(body)-4 > limit || binary.LittleEndian.Uint32(body) != uint32(len(body)-4) {
		return "", false
	}
	raw := body[4:]
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", false
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return "", false
	}
	canonical, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	if err != nil || !bytes.Equal(canonical, raw) {
		return "", false
	}
	return string(decoded), true
}

func currentGuildTextError(err error) byte {
	switch {
	case errors.Is(err, dnfguild.ErrNotMember), errors.Is(err, dnfguild.ErrCharacterNotFound):
		return 34
	case errors.Is(err, dnfguild.ErrPermissionDenied):
		return 36
	case errors.Is(err, dnfguild.ErrInvalidText):
		return 108
	default:
		return currentGuildGenericError
	}
}

func (s *Service) handleCurrentGuildNotice(session *gameSession, body []byte) error {
	return s.handleCurrentGuildText(session, body, true)
}

func (s *Service) handleCurrentGuildMemberMemo(session *gameSession, body []byte) error {
	return s.handleCurrentGuildText(session, body, false)
}

func (s *Service) handleCurrentGuildText(session *gameSession, body []byte, notice bool) error {
	op, limit := uint16(dnfenum.CmdPacketWriteGuildMemberMemo), dnfguild.MemberMemoMaxBytes
	if notice {
		op, limit = uint16(dnfenum.CmdPacketNotifyMessageToGuild), dnfguild.NoticeMaxBytes
	}
	text, ok := parseCurrentGuildText(body, limit)
	if !ok {
		return s.sendGameUpperFailure(session, op, 108)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, 34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var guildID uint32
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return dnfguild.ErrCharacterNotFound
		}
		owner := dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}
		var g *dnfguild.Guild
		var err error
		if notice {
			g, err = r.SetNotice(owner, text, s.gameplayNow())
		} else {
			g, err = r.SetMemberMemo(owner, text, s.gameplayNow())
		}
		if err == nil {
			guildID = g.ID
		}
		return err
	})
	return s.finishCurrentGuildEdit(ctx, source, guildID, op, notice, err, currentGuildTextError(err))
}

func (s *Service) finishCurrentGuildEdit(ctx context.Context, source boundGameSessionCharacter, guildID uint32, op uint16, notice bool, err error, failureCode byte) error {
	// The mutation releases its reservation before delivery. Recheck the exact
	// generation so a reconnect or character switch cannot receive the old ACK.
	release, reserveErr := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if reserveErr != nil {
		return nil
	}
	var sendErr error
	if err != nil {
		sendErr = s.sendGameUpperFailure(source.session, op, failureCode)
	} else {
		sendErr = s.sendGameUpperSuccess(source.session, op, nil)
	}
	release()
	if err == nil {
		// Never hold the sender while reserving peers: simultaneous edits must
		// not acquire each other's character reservations in opposite order.
		s.refreshCurrentGuildText(ctx, guildID, source, notice)
	}
	return sendErr
}

func (s *Service) refreshCurrentGuildText(ctx context.Context, guildID uint32, source boundGameSessionCharacter, notice bool) {
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		s.logGameEvent(source.session, "game-guild-text-refresh-failed", "error", err)
		return
	}
	g, found := r.GuildByID(guildID)
	if !found {
		return
	}
	for _, member := range dnfguild.SortedMembers(g) {
		id, err := strconv.ParseUint(member.CharacterID, 10, 16)
		if err != nil || id == 0 {
			continue
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(uint16(id))
		if !online {
			continue
		}
		if err := s.refreshCurrentGuildTextPeer(ctx, guildID, peer, source, notice); err != nil {
			s.logGameEvent(peer.session, "game-guild-text-refresh-failed", "error", err)
		}
	}
}

func (s *Service) refreshCurrentGuildTextPeer(ctx context.Context, guildID uint32, peer, source boundGameSessionCharacter, notice bool) error {
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{peer})
	if err != nil {
		return err
	}
	defer release()
	character, err := s.currentGuildOwnedSelectedCharacter(peer.session)
	if err != nil {
		return err
	}
	_, r, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, _, found := r.GuildForOwner(dnfrepo.GuildOwner{CharacterID: character.CharacterID, AccountID: character.AccountID})
	if !found || g.ID != guildID {
		return nil
	}
	if notice {
		return s.sendCurrentGuildManagementState(peer.session, g)
	}
	peer.session.guild.mu.Lock()
	requested := peer.session.guild.memberListRequested
	peer.session.guild.mu.Unlock()
	if requested || peer == source {
		return s.handleCurrentGuildMemberListForOpcode(peer.session, uint16(dnfenum.CmdPacketGuildAllMemberList))
	}
	return nil
}
