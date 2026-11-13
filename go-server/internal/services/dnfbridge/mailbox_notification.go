package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/mail"
)

// sendMailboxAlarmForSession projects a mailbox's durable unread state onto
// one already-active game session. It is used for both a live recipient and
// the first selected-town scene, which must explicitly clear a prior actor's
// indicator by sending a zero count as well.
func (s *Service) sendMailboxAlarmForSession(session *gameSession, characterID uint16, source string) error {
	if session == nil || characterID == 0 {
		return nil
	}
	repos, available := s.repositoryGroup()
	if !available {
		return fmt.Errorf("mailbox alarm repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	unread, err := mail.UnreadCount(ctx, repos, strconv.FormatUint(uint64(characterID), 10), s.gameplayNow())
	if err != nil {
		return fmt.Errorf("load mailbox alarm state: %w", err)
	}
	if err := s.sendGameUpperRawClass(
		session,
		mail.MailboxAlarmNotificationMessageID,
		mail.BuildAlarmNotification(unread),
		0,
	); err != nil {
		return err
	}
	s.logGameEvent(session, "game-mailbox-alarm-sent",
		"source", source,
		"char_id", characterID,
		"unread", unread)
	return nil
}

// sendMailboxAlarmToOnlineRecipient is deliberately a bridge-owned delivery
// projection: mailbox ownership has already committed in the mail module, and
// an offline recipient receives the same class0/op63 snapshot during the
// selected-town bootstrap. The game-session index and its wire mutex make the
// optional live notice safe without giving the domain module transport
// ownership.
func (s *Service) sendMailboxAlarmToOnlineRecipient(characterID uint16) error {
	identity, online := s.onlineGameSessionCharacterSnapshot(characterID)
	if !online {
		return nil
	}
	return s.tryPostGameSessionCharacterEvent(identity.session, "mailbox-delivery-alarm", identity.character, identity.generation, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		return s.sendMailboxAlarmForSession(identity.session, characterID, "online_recipient_mail_delivery")
	})
}
