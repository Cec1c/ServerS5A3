package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentMonthlyAttendanceEventID uint16 = 2370
const currentMonthlyAttendanceClaimID uint16 = 1294

// v96 0226FCF4 maps event2370 to native badge1127; 022760C4 maps that
// badge to 02265AB0, which toggles native window290. No synthetic XUI route.
type currentMonthlyAttendanceProjection struct {
	characterID         uint16
	characterGeneration uint64
	date                string
	body                [37]byte
}

func (s *Service) currentMonthlyAttendanceEnabled() bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || s.activityControls == nil || !s.activityControls.Enabled(currentMonthlyAttendanceEventID) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	_, err := s.currentMonthlyCatalog(ctx)
	return err == nil
}

func (s *Service) currentMonthlyCatalog(ctx context.Context) (*onlineevent.MonthlyCatalog, error) {
	if s == nil {
		return nil, onlineevent.ErrOwnerUnavailable
	}
	s.monthlyCatalogMu.Lock()
	defer s.monthlyCatalogMu.Unlock()
	if s.monthlyCatalog != nil {
		return s.monthlyCatalog, nil
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
	// must not advertise event2370 even if an operator enables the control.
	if _, err := archive.ReadText("ui/event/chn_event_2016/160825_daily_attendance/eventdailyattendancewindow.xui"); err != nil {
		return nil, err
	}
	catalog, err := onlineevent.LoadMonthlyCatalog(ctx, archive)
	if err != nil {
		return nil, err
	}
	s.monthlyCatalog = catalog
	return catalog, nil
}

// Actual01055360/010559D7 -> 011139A0 consumes five u32 values.
func buildCurrentMonthlyAttendanceBody(state onlineevent.MonthlySnapshot) []byte {
	var w packetWriter
	w.writeUint32(uint32(currentMonthlyAttendanceEventID))
	w.writeByte(0)
	w.writeUint32(state.Days)
	for _, v := range state.Bonus {
		w.writeUint32(v)
	}
	w.writeUint32(state.Clears)
	w.writeBytes(make([]byte, 12))
	return w.bytes()
}

func (s *Service) sendCurrentMonthlyAttendance(session *gameSession, claim bool) error {
	if !s.currentMonthlyAttendanceEnabled() {
		return onlineevent.ErrOwnerUnavailable
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentMonthlyCatalog(ctx)
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
	now := s.gameplayNow()
	charID := strconv.Itoa(int(identity.character))
	state, err := onlineevent.SettleMonthlyAttendance(ctx, repositories, catalog, onlineevent.MonthlyCommand{
		AccountID: s.accountIDForSession(session), CharacterID: charID, Now: now, ClaimBonus: claim,
		Validate: func() bool { return s.boundGameSessionCharacterCurrent(identity) },
		Deliver: func(mailboxes repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
			definition, err := items.ResolveItem(uint32(reward.ItemID))
			if err != nil {
				return "", err
			}
			if definition.Kind != dungeonDropItemStackable {
				return "", fmt.Errorf("monthly reward item%d unsupported", reward.ItemID)
			}
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
			if err != nil {
				return "", err
			}
			return repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{RecipientCharacterID: charID, Title: "每日签到奖励", Body: "推荐地下城签到奖励已发放，请查收附件。", Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}}})
		},
	})
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return onlineevent.ErrOwnerUnavailable
	}
	if claim {
		if err := s.sendGameUpperRawClass(session, currentMonthlyAttendanceClaimID, []byte{1}, 1); err != nil {
			return err
		}
	}
	projection := currentMonthlyAttendanceProjection{characterID: identity.character, characterGeneration: identity.generation, date: now.In(onlineevent.ChinaCalendar).Add(-6 * time.Hour).Format("2006-01-02"), body: [37]byte(buildCurrentMonthlyAttendanceBody(state))}
	if projection != session.monthlyAttendance {
		if err := s.sendGameUpperRawClass(session, currentSpendTimeProgressMsgID, projection.body[:], 0); err != nil {
			return err
		}
		session.monthlyAttendance = projection
	}
	if len(state.MailIDs) > 0 {
		s.logGameEvent(session, "game-monthly-attendance-mail", "char_id", identity.character, "days", state.Days, "mail_count", len(state.MailIDs))
		return s.sendMailboxAlarmForSession(session, identity.character, "monthly_attendance")
	}
	return nil
}

// Actual01113880 sends exactly13 opaque bytes. None authorize an item/count.
// Class1 callback00769AD0 is a plain RET; op1206 owns displayed state.
func (s *Service) handleCurrentMonthlyAttendanceClaim(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || op != currentMonthlyAttendanceClaimID {
		return false, nil
	}
	if classification != 1 {
		return true, nil
	}
	if len(body) != 13 || !s.currentMonthlyAttendanceEnabled() {
		return true, s.sendGameUpperFailure(session, op, 0)
	}
	if err := s.sendCurrentMonthlyAttendance(session, true); err != nil {
		s.logGameEvent(session, "game-monthly-attendance-rejected", "error", err)
		return true, s.sendGameUpperFailure(session, op, 0)
	}
	return true, nil
}

func (s *Service) armCurrentMonthlyAttendanceTick(session *gameSession) error {
	if !s.currentMonthlyAttendanceEnabled() || session == nil || session.selectedCharacterID == 0 {
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
	return queue.ScheduleAfter(fmt.Sprintf("dnf-monthly-attendance:%s", session.connID), 30*time.Second, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "monthly-attendance-retry", charID, generation, func() error {
			if err := s.sendCurrentMonthlyAttendance(session, false); err != nil {
				s.logGameEvent(session, "game-monthly-attendance-deferred", "error", err)
			}
			return s.armCurrentMonthlyAttendanceTick(session)
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "game-monthly-attendance-event-submit-failed", "error", err)
		}
	})
}
func (s *Service) cancelCurrentMonthlyAttendanceTick(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("dnf-monthly-attendance:%s", session.connID))
	}
}
