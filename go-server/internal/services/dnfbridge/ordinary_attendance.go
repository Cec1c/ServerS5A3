package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
	"time"
)

const currentOrdinaryAttendanceEventID uint16 = 331

func (s *Service) currentOrdinaryAttendanceEnabled() bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || s.activityControls == nil || !s.activityControls.Enabled(331) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	_, err := s.currentOrdinaryCatalog(ctx)
	return err == nil
}

func (s *Service) currentOrdinaryCatalog(ctx context.Context) (*onlineevent.OrdinaryCatalog, error) {
	if s == nil {
		return nil, onlineevent.ErrOwnerUnavailable
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if archive == nil {
		return nil, onlineevent.ErrOwnerUnavailable
	}
	mainUI, err := archive.ReadText("ui/event/attendancedailyevent/main.xui")
	if err != nil {
		return nil, err
	}
	slotUI, err := archive.ReadText(ordinaryAttendanceSlotPath)
	if err != nil {
		return nil, err
	}
	if err := validateOrdinaryAttendanceUI(mainUI, slotUI); err != nil {
		return nil, err
	}
	return onlineevent.LoadOrdinaryCatalog(ctx, archive)
}

func buildCurrentOrdinaryAttendanceBody(state onlineevent.OrdinarySnapshot) []byte {
	body := make([]byte, 9)
	if state.CanClaim {
		body[0] = 1
	}
	binary.LittleEndian.PutUint32(body[1:], state.Days)
	binary.LittleEndian.PutUint32(body[5:], state.Claimed)
	return body
}

// The actual22F0030 failure consumer reads owner even when status is failure.
// Its success path reads owner and three reserved u32; generic failure is short.
func buildCurrentOrdinaryAttendanceACK(success bool) []byte {
	if !success {
		body := make([]byte, 6)
		body[1] = 1
		binary.LittleEndian.PutUint32(body[2:], 331)
		return body
	}
	body := make([]byte, 17)
	body[0] = 1
	binary.LittleEndian.PutUint32(body[1:], 331)
	return body
}

func (s *Service) settleCurrentOrdinaryAttendance(session *gameSession, register, claim bool) (onlineevent.OrdinarySnapshot, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return onlineevent.OrdinarySnapshot{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentOrdinaryCatalog(ctx)
	if err != nil {
		return onlineevent.OrdinarySnapshot{}, err
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.OrdinarySnapshot{}, onlineevent.ErrOwnerUnavailable
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return onlineevent.OrdinarySnapshot{}, err
	}
	now := s.gameplayNow()
	charID := strconv.Itoa(int(identity.character))
	state, err := onlineevent.SettleOrdinaryAttendance(ctx, repositories, catalog, onlineevent.OrdinaryCommand{
		AccountID: s.accountIDForSession(session), CharacterID: charID, Now: now, RegisterDay: register, Claim: claim,
		Validate: func() bool { return s.boundGameSessionCharacterCurrent(identity) },
		Deliver: func(mailboxes repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
			definition, err := items.ResolveItem(uint32(reward.ItemID))
			if err != nil {
				return "", err
			}
			if definition.Kind != dungeonDropItemStackable {
				return "", fmt.Errorf("ordinary reward item%d unsupported", reward.ItemID)
			}
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
			if err != nil {
				return "", err
			}
			return repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{RecipientCharacterID: charID, Title: "普通签到奖励", Body: "签到奖励已发放，请查收附件。", Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}}})
		}})
	if err != nil {
		return onlineevent.OrdinarySnapshot{}, err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return onlineevent.OrdinarySnapshot{}, onlineevent.ErrOwnerUnavailable
	}
	return state, nil
}

func (s *Service) handleCurrentOrdinaryAttendanceClaim(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || op != 688 || len(body) < 4 || binary.LittleEndian.Uint32(body) != 331 {
		return false, nil
	}
	if classification != 1 {
		return true, nil
	}
	if len(body) != 4 || !s.currentOrdinaryAttendanceEnabled() {
		return true, s.sendGameUpperRawClass(session, 688, buildCurrentOrdinaryAttendanceACK(false), 1)
	}
	state, err := s.settleCurrentOrdinaryAttendance(session, false, true)
	if err != nil {
		s.logGameEvent(session, "game-ordinary-attendance-rejected", "error", err)
		return true, s.sendGameUpperRawClass(session, 688, buildCurrentOrdinaryAttendanceACK(false), 1)
	}
	if err := s.sendGameUpperRawClass(session, 688, buildCurrentOrdinaryAttendanceACK(true), 1); err != nil {
		return true, err
	}
	if err := s.sendGameUpperRawClass(session, 1386, buildCurrentOrdinaryAttendanceBody(state), 0); err != nil {
		return true, err
	}
	if len(state.MailIDs) > 0 {
		return true, s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "ordinary_attendance")
	}
	return true, nil
}

func (s *Service) sendCurrentOrdinaryAttendance(session *gameSession) error {
	if !s.currentOrdinaryAttendanceEnabled() {
		return onlineevent.ErrOwnerUnavailable
	}
	state, err := s.settleCurrentOrdinaryAttendance(session, true, false)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 1386, buildCurrentOrdinaryAttendanceBody(state), 0)
}

func (s *Service) armCurrentOrdinaryAttendanceTick(session *gameSession) error {
	if !s.currentOrdinaryAttendanceEnabled() || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	charID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return err
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	return queue.ScheduleAfter(fmt.Sprintf("dnf-ordinary-attendance:%s", session.connID), 30*time.Second, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "ordinary-attendance-refresh", charID, generation, func() error {
			if err := s.sendCurrentOrdinaryAttendance(session); err != nil {
				s.logGameEvent(session, "game-ordinary-attendance-deferred", "error", err)
			}
			return s.armCurrentOrdinaryAttendanceTick(session)
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "game-ordinary-attendance-event-submit-failed", "error", err)
		}
	})
}

func (s *Service) cancelCurrentOrdinaryAttendanceTick(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("dnf-ordinary-attendance:%s", session.connID))
	}
}
