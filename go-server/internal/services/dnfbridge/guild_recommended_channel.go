package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var errCurrentGuildRecommendedChannelUnavailable = errors.New("guild recommended channel is not advertised")

func (s *Service) currentGuildRecommendedChannel(session *gameSession) (channelcatalog.Channel, bool) {
	if s == nil || session == nil || session.channel.ID <= 0 {
		return channelcatalog.Channel{}, false
	}
	catalog := s.currentCatalog()
	if catalog == nil {
		return channelcatalog.Channel{}, false
	}
	channel, found := catalog.Channel(session.channel.ID)
	if !found || channel.ID != session.channel.ID || !channelcatalog.IsResidentChannel(channel) {
		return channelcatalog.Channel{}, false
	}
	for _, advertised := range s.channelsForRequest(dnfenum.GroupCain) {
		if advertised.ID == channel.ID {
			return channel, true
		}
	}
	return channelcatalog.Channel{}, false
}

func currentGuildRecommendedChannelError(err error) byte {
	switch {
	case errors.Is(err, dnfguild.ErrPermissionDenied):
		return 19
	case errors.Is(err, dnfguild.ErrInvalidRecommendedChannel),
		errors.Is(err, errCurrentGuildRecommendedChannelUnavailable):
		return 7
	default:
		return currentGuildGenericError
	}
}

func (s *Service) handleCurrentGuildRecommendedChannel(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketSetGuildRecommandChannel)
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, currentGuildGenericError)
	}
	region := s.channelAdvertiseID()
	if region < 0 || region > 249 {
		return s.sendGameUpperFailure(session, op, 7)
	}
	if _, ok := s.currentGuildRecommendedChannel(session); !ok {
		return s.sendGameUpperFailure(session, op, 7)
	}

	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var guildID uint32
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return dnfguild.ErrCharacterNotFound
		}
		channel, ok := s.currentGuildRecommendedChannel(session)
		if !ok {
			return errCurrentGuildRecommendedChannelUnavailable
		}
		g, err := r.SetRecommendedChannel(
			dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID},
			uint32(region),
			uint32(channel.ID),
			s.gameplayNow(),
		)
		if err == nil {
			guildID = g.ID
		}
		return err
	})
	return s.finishCurrentGuildEdit(
		ctx,
		source,
		guildID,
		op,
		true,
		err,
		currentGuildRecommendedChannelError(err),
	)
}
