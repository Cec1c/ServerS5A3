package dnfbridge

import (
	"context"
	"strconv"
	"time"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
)

func (s *Service) currentLaborDayCatalog(ctx context.Context) (*dnfonlineevent.LaborDayCatalog, error) {
	s.laborDayMu.Lock()
	defer s.laborDayMu.Unlock()
	if s.laborDayCatalog != nil {
		return s.laborDayCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	repos, ok := s.repositoryGroup()
	if archive == nil || !ok || repos.Settings == nil {
		return nil, nil
	}
	c, err := dnfonlineevent.LoadLaborDayCatalog(ctx, archive)
	if err != nil {
		return nil, err
	}
	p := s.currentPortedPolicy()
	c.Policy = dnfonlineevent.LaborDayPolicy{Starts: p.LaborDay.Starts, Ends: p.LaborDay.Ends}
	s.laborDayCatalog = c
	s.logPacketEvent("劳动节活动时间配置", "starts", c.Policy.Starts, "ends", c.Policy.Ends, "daily_seconds", 600, "daily_limit", 10)
	return c, nil
}

func (s *Service) preloadLaborDayCatalog(ctx context.Context) error {
	_, err := s.currentLaborDayCatalog(ctx)
	return err
}

func (s *Service) settleCurrentLaborDay(ctx context.Context, session *gameSession, c *dnfonlineevent.LaborDayCatalog, from, to time.Time, login bool) error {
	if c == nil {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfonlineevent.ErrOwnerUnavailable
	}
	characterID := session.spendTime.characterID
	if characterID == 0 {
		characterID = session.selectedCharacterID
	}
	id := strconv.Itoa(int(characterID))
	now := s.gameplayNow()
	state, err := dnfonlineevent.SettleLaborDay(ctx, repos, c, dnfonlineevent.LaborDayCommand{
		AccountID: s.accountIDForSession(session), CharacterID: id, Now: now, From: from, To: to, Login: login,
		Validate: s.currentPortedRequestValidator(session, characterID, false),
		Deliver:  s.currentPortedMailDelivery(ctx, session, id, "狂热劳动节豪礼大放送", "劳动节奖励已送达，请查收附件。", "labor_day_2348"),
	})
	if err != nil {
		return err
	}
	if len(state.MailIDs) > 0 {
		s.logGameEvent(session, "劳动节活动奖励发放", "login_days", state.LoginDays, "today_seconds", state.TodaySeconds, "today_paid", state.TodayPaid, "mail_count", len(state.MailIDs))
		// Mail has committed. An alarm failure must not prevent the observation
		// cursor advancing; the mailbox is authoritative on the next login.
		if err := s.sendMailboxAlarmForSession(session, characterID, "labor_day_2348"); err != nil {
			s.logGameEvent(session, "劳动节邮件提醒暂缓", "error", err)
		}
	}
	return nil
}
