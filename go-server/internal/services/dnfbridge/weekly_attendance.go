package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentWeeklyAttendanceEventID uint16 = 2208
const currentWeeklyAttendanceClaimID uint16 = 1113

// v96 0226F9AB maps event2208 to badge1083, whose022651E0 opens window26F.
type currentWeeklyAttendanceProjection struct {
	characterID         uint16
	characterGeneration uint64
	date                string
	body                [459]byte
}

func (s *Service) currentWeeklyAttendanceEnabled() bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || s.activityControls == nil || !s.activityControls.Enabled(currentWeeklyAttendanceEventID) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	_, err := s.currentWeeklyCatalog(ctx)
	return err == nil
}

func (s *Service) currentWeeklyCatalog(ctx context.Context) (*onlineevent.WeeklyCatalog, error) {
	if s == nil {
		return nil, onlineevent.ErrOwnerUnavailable
	}
	s.weeklyCatalogMu.Lock()
	defer s.weeklyCatalogMu.Unlock()
	if s.weeklyCatalog != nil {
		return s.weeklyCatalog, nil
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
	// The native window factory owns this exact XUI. An archive lacking it
	// must not advertise event2208 even if an operator enables the control.
	if _, err := archive.ReadText("ui/event/chn_event_2018/chn_eventtotalattendance_20181101/eventtotalattendancecheckwindow.xui"); err != nil {
		return nil, err
	}
	catalog, err := onlineevent.LoadWeeklyCatalog(ctx, archive)
	if err != nil {
		return nil, err
	}
	s.weeklyCatalog = catalog
	return catalog, nil
}

func buildCurrentWeeklyAttendanceBody(state onlineevent.WeeklySnapshot) []byte {
	b := make([]byte, 459)
	binary.LittleEndian.PutUint32(b[4:], state.Total)
	binary.LittleEndian.PutUint32(b[8:], state.BonusMask)
	copy(b[0x19c:], state.SignedWeeks[:])
	binary.LittleEndian.PutUint32(b[0x1b4:], state.Clears)
	binary.LittleEndian.PutUint32(b[0x1bc:], state.Catchups)
	if state.SignedThisWeek {
		binary.LittleEndian.PutUint32(b[0x1c0:], 1)
	}
	binary.LittleEndian.PutUint32(b[0x1c4:], state.Week)
	if state.CanSign {
		b[0x1c8] = 1
	}
	if state.CanCatchup {
		b[0x1c9] = 1
	}
	if state.CanBonus {
		b[0x1ca] = 1
	}
	return b
}
func (s *Service) currentWeeklySeason(session *gameSession) (onlineevent.WeeklySeason, error) {
	if !s.currentWeeklyAttendanceEnabled() {
		return onlineevent.WeeklySeason{}, onlineevent.ErrOwnerUnavailable
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return onlineevent.WeeklySeason{}, onlineevent.ErrOwnerUnavailable
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.WeeklySeason{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	return onlineevent.ActivateWeeklySeason(ctx, g, s.accountIDForSession(session), strconv.Itoa(int(identity.character)), s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) && s.currentWeeklyAttendanceEnabled() })
}
func (s *Service) sendCurrentWeeklyAttendance(session *gameSession, claim, bonus bool) error {
	if !s.currentWeeklyAttendanceEnabled() {
		return onlineevent.ErrOwnerUnavailable
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentWeeklyCatalog(ctx)
	if err != nil {
		return err
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.ErrOwnerUnavailable
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	season, err := s.currentWeeklySeason(session)
	if err != nil {
		return err
	}
	now := s.gameplayNow()
	charID := strconv.Itoa(int(identity.character))
	state, err := onlineevent.SettleWeeklyAttendance(ctx, repositories, catalog, onlineevent.WeeklyCommand{
		AccountID: s.accountIDForSession(session), CharacterID: charID, Now: now, Season: season, Claim: claim, ClaimBonus: bonus,
		Validate: func() bool { return s.boundGameSessionCharacterCurrent(identity) },
		Deliver: func(mailboxes repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
			definition, err := items.ResolveItem(uint32(reward.ItemID))
			if err != nil {
				return "", err
			}
			if definition.Kind != dungeonDropItemStackable {
				return "", fmt.Errorf("weekly reward item%d unsupported", reward.ItemID)
			}
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
			if err != nil {
				return "", err
			}
			return repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{RecipientCharacterID: charID, Title: "每周签到奖励", Body: "推荐地下城签到奖励已发放，请查收附件。", Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}}})
		},
	})
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return onlineevent.ErrOwnerUnavailable
	}
	if claim {
		if err := s.sendGameUpperRawClass(session, currentWeeklyAttendanceClaimID, func() []byte {
			b := make([]byte, 5)
			b[0] = 1
			if len(state.MailIDs) == 0 {
				binary.LittleEndian.PutUint32(b[1:], 1)
			}
			return b
		}(), 1); err != nil {
			return err
		}
	}
	projection := currentWeeklyAttendanceProjection{characterID: identity.character, characterGeneration: identity.generation, date: now.In(onlineevent.ChinaCalendar).Add(-6 * time.Hour).Format("2006-01-02"), body: [459]byte(buildCurrentWeeklyAttendanceBody(state))}
	if projection != session.weeklyAttendance {
		if err := s.sendGameUpperRawClass(session, 1093, projection.body[:], 0); err != nil {
			return err
		}
		session.weeklyAttendance = projection
	}
	if len(state.MailIDs) > 0 {
		s.logGameEvent(session, "game-weekly-attendance-mail", "char_id", identity.character, "signed", state.Total, "mail_count", len(state.MailIDs))
		return s.sendMailboxAlarmForSession(session, identity.character, "weekly_attendance")
	}
	return nil
}

// Actual17C9E4B sends1113 opaque13B or689 owner2208+three zero u32.
// No ACK689 consumer is proved;1093 owns the displayed bonus state.
func (s *Service) handleCurrentWeeklyAttendanceClaim(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false, nil
	}
	bonus := op == 689
	if op != 1113 && !bonus {
		return false, nil
	}
	if bonus && (len(body) < 4 || binary.LittleEndian.Uint32(body) != 2208) {
		return false, nil
	}
	if classification != 1 {
		return true, nil
	}
	valid := len(body) == 13
	if bonus {
		valid = len(body) == 16 && binary.LittleEndian.Uint32(body[4:]) == 0 && binary.LittleEndian.Uint32(body[8:]) == 0 && binary.LittleEndian.Uint32(body[12:]) == 0
	}
	if !valid || !s.currentWeeklyAttendanceEnabled() {
		if bonus {
			return true, nil
		}
		return true, s.sendGameUpperFailure(session, op, 1)
	}
	if err := s.sendCurrentWeeklyAttendance(session, !bonus, bonus); err != nil {
		s.logGameEvent(session, "game-weekly-attendance-rejected", "error", err)
		if bonus {
			return true, nil
		}
		return true, s.sendGameUpperFailure(session, op, 1)
	}
	return true, nil
}

func (s *Service) armCurrentWeeklyAttendanceTick(session *gameSession) error {
	if !s.currentWeeklyAttendanceEnabled() || session == nil || session.selectedCharacterID == 0 {
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
	return queue.ScheduleAfter(fmt.Sprintf("dnf-weekly-attendance:%s", session.connID), 30*time.Second, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "weekly-attendance-retry", charID, generation, func() error {
			if err := s.sendCurrentWeeklyAttendance(session, false, false); err != nil {
				s.logGameEvent(session, "game-weekly-attendance-deferred", "error", err)
			}
			return s.armCurrentWeeklyAttendanceTick(session)
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "game-weekly-attendance-event-submit-failed", "error", err)
		}
	})
}
func (s *Service) cancelCurrentWeeklyAttendanceTick(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("dnf-weekly-attendance:%s", session.connID))
	}
}
