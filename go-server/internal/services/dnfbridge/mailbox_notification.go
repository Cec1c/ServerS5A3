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

// queueMailboxAlarmForOnlineRecipient is deliberately a bridge-owned delivery
// projection: mailbox ownership has already committed in the mail module or a
// separate process sharing the repository, and an offline recipient receives
// the same class0/op63 snapshot during the selected-town bootstrap. The
// game-session index and its wire mutex make the optional live notice safe
// without giving either writer transport ownership.
func (s *Service) queueMailboxAlarmForOnlineRecipient(characterID uint16) (bool, error) {
	identity, online := s.onlineGameSessionCharacterSnapshot(characterID)
	if !online {
		return false, nil
	}
	err := s.tryPostGameSessionCharacterEvent(identity.session, "mailbox-delivery-alarm", identity.character, identity.generation, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		return s.sendMailboxAlarmForSession(identity.session, characterID, "online_recipient_mail_delivery")
	})
	return true, err
}

func (s *Service) sendMailboxAlarmToOnlineRecipient(characterID uint16) error {
	_, err := s.queueMailboxAlarmForOnlineRecipient(characterID)
	return err
}

// The initial type1345 tail can precede the client's finish-loading request.
// Its alarm does not prove that the new scene's UI consumed the count. Read
// the durable count again once the scene's state and post-state are installed;
// never retain the earlier count across mail delivery during loading.
// This remains an intentionally one-shot, best-effort projection. It does not
// schedule a timer or resend after an ambiguous wire failure: such a failure
// cannot prove whether the count packet reached the client. A failure leaves
// mailboxSceneAlarmSent clear so a later client op37 or a new scene may read the
// durable count again; this is not an automatic network-recovery guarantee.
func (s *Service) resyncMailboxAlarmAfterSceneReady(session *gameSession, source string) {
	if session == nil || session.selectedCharacterID == 0 {
		return
	}
	session.finishLoadingMu.Lock()
	needed := session.currentFinishLoadingStateSent && session.postFinishLoadingPlayerStateSent && !session.mailboxSceneAlarmSent
	revision := session.finishLoadingRevision
	session.finishLoadingMu.Unlock()
	if !needed {
		return
	}
	if err := s.sendMailboxAlarmForSession(session, session.selectedCharacterID, source+"_scene_ready_mailbox"); err != nil {
		// A projection failure must not fail an already committed scene entry.
		s.logWarn("dnfbridge scene-ready mailbox alarm failed; no scheduled retry", "char_id", session.selectedCharacterID, "error", err)
		return
	}
	session.finishLoadingMu.Lock()
	if session.finishLoadingRevision == revision {
		session.mailboxSceneAlarmSent = true
	}
	session.finishLoadingMu.Unlock()
}
