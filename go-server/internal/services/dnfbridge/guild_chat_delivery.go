package dnfbridge

import (
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/mail"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

type currentGuildChatRecipient struct {
	identity boundGameSessionCharacter
	account  string
	joinedAt time.Time
}

// Only one recipient's binding is pinned, and only for this bounded delivery.
// Submit never waits for another event loop: simultaneous senders cannot wait
// on each other's full queues. Offline, stale and overloaded peers are local
// delivery failures; they cannot cancel the sender or healthy recipients.
func (s *Service) queueCurrentGuildChat(peer currentGuildChatRecipient, guildID uint32, body []byte) error {
	return s.queueCurrentGuildChatNotice(peer, guildID, currentOfficialWhisperNotifyOpcode, body)
}

func (s *Service) queueCurrentGuildChatNotice(peer currentGuildChatRecipient, guildID uint32, opcode uint16, body []byte) error {
	p := peer.identity.session
	if p == nil || p.loginRevoked.Load() {
		return errInvalidGameSessionEvent
	}
	body = append([]byte(nil), body...)
	expires := time.Now().Add(createWriteTimeout)
	run := func() error {
		ctx, cancel := guildChatContext(p)
		defer cancel()
		ctx, expireCancel := context.WithDeadline(ctx, expires)
		defer expireCancel()
		err := s.deliverCurrentGuildChatNotice(ctx, peer, guildID, opcode, body)
		if err != nil {
			s.logWarn("dnfbridge guild chat recipient unavailable", "target_char_id", peer.identity.character, "stage", "delivery", "error", err)
		}
		return nil // Peer failure never terminates an unrelated sender's loop.
	}
	if p.events == nil || p.events.loop == nil {
		return run()
	}
	if !p.events.active.Load() {
		return errInvalidGameSessionEvent
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return p.events.loop.Submit(eventloop.Event{
		Type: "game-guild-chat-delivery", Priority: eventloop.PriorityMedium, Context: ctx,
		Data: gameSessionEventTask{source: "game-guild-chat-delivery", characterID: peer.identity.character, characterGeneration: peer.identity.generation, async: true, run: run},
	})
}

func (s *Service) deliverCurrentGuildChat(ctx context.Context, peer currentGuildChatRecipient, guildID uint32, body []byte) error {
	return s.deliverCurrentGuildChatNotice(ctx, peer, guildID, currentOfficialWhisperNotifyOpcode, body)
}

func (s *Service) deliverCurrentGuildChatNotice(ctx context.Context, peer currentGuildChatRecipient, guildID uint32, opcode uint16, body []byte) error {
	reservation, err := s.reserveCurrentCharacterAssets(ctx, peer.identity, peer.account)
	if err != nil {
		return err
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	validate := func() error {
		repos, reg, err := s.loadCurrentGuildRegistry(ctx)
		if err != nil {
			return err
		}
		id := strconv.Itoa(int(peer.identity.character))
		g, m, found := reg.GuildForCharacter(id)
		if !found || g.ID != guildID || m.AccountID != peer.account || !m.JoinedAt.Equal(peer.joinedAt) {
			return guild.ErrNotMember
		}
		actor, found, err := repos.Character.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != peer.account || !s.boundGameSessionCharacterCurrent(peer.identity) {
			return guild.ErrCharacterNotFound
		}
		return nil
	}
	return s.sendCurrentGuildChatFrameChecked(ctx, peer.identity.session, opcode, body, 0, validate)
}

// Sequence allocation, DPROTO and the socket write stay under the existing
// wire mutex. A partial/failed write closes just this stream, which cannot be
// reused after its codec state or sequence has advanced.
func (s *Service) sendCurrentGuildChatFrame(ctx context.Context, p *gameSession, op uint16, body []byte, class byte) error {
	return s.sendCurrentGuildChatFrameChecked(ctx, p, op, body, class, nil)
}

func (s *Service) sendCurrentGuildChatFrameChecked(ctx context.Context, p *gameSession, op uint16, body []byte, class byte, validate func() error) error {
	if p == nil || p.conn == nil {
		return errInvalidGameSessionEvent
	}
	if err := s.lockGameWire(p, ctx); err != nil {
		return err
	}
	defer p.wireMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A wire wait may overlap a kick/leave. Check the latest membership after
	// that wait, before allocating a sequence or advancing the codec.
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	finishWrite, err := beginGameWriteBudgetLocked(p, ctx)
	if err != nil {
		return err
	}
	defer finishWrite()
	err = s.sendGameUpperRawClassCodecLocked(p, op, body, class, true)
	if err != nil {
		p.closeGameTransport()
		return err
	}
	return nil
}

// Contribution coins are permanent main-inventory quest stacks. Reuse the
// standard row/mail/points encoders, reading the committed state, but give
// their optional chat projection the same bounded socket path as the text.
func (s *Service) projectCurrentGuildChatContribution(identity boundGameSessionCharacter, income guild.ContributionIncome) error {
	if income.Points == 0 {
		return nil
	}
	p := identity.session
	ctx, cancel := guildChatContext(p)
	defer cancel()
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, s.accountIDForSession(p))
	if err != nil {
		return err
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	repos, ok := s.repositoryGroup()
	if !ok {
		return guild.ErrRepositoryUnavailable
	}
	id := strconv.Itoa(int(identity.character))
	if income.Delivery.MailID != "" {
		result, err := mail.NewOwner(repos).Open(ctx, id)
		if err != nil {
			return err
		}
		if err = s.sendCurrentGuildChatFrame(ctx, p, mail.MailboxAlarmNotificationMessageID, mail.BuildAlarmNotification(result.Unread), 0); err != nil {
			return err
		}
	} else {
		if income.Delivery.Slot < 0 || repos.Inventory == nil {
			return guild.ErrContributionState
		}
		inventory, found, err := repos.Inventory.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return guild.ErrContributionState
		}
		key := "0:" + strconv.Itoa(int(income.Delivery.Slot))
		stack, found := inventory.Slots[key]
		if !found || stack.ItemID != guild.ContributionCoinID {
			return guild.ErrContributionState
		}
		entry := currentItemListEntryFromStack(r.MainInventoryListType, income.Delivery.Slot, stack)
		if err = s.sendCurrentGuildChatFrame(ctx, p, 14, buildCurrentItemUpdateBody(r.MainInventoryListType, []currentItemListEntry{entry}), 0); err != nil {
			return err
		}
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, _, found := reg.GuildForOwner(r.GuildOwner{AccountID: s.accountIDForSession(p), CharacterID: id})
	if !found {
		return nil
	}
	body, err := buildCurrentGuildHongbaoPointsBody(guild.SortedMembers(g))
	if err != nil {
		return err
	}
	return s.sendCurrentGuildChatFrame(ctx, p, currentGuildHongbaoPointsOpcode, upperSuccessBody(body), 1)
}
