package dnfbridge

import (
	"context"
	"encoding/json"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestWeeklyLegal1113DispatchRedBeforeImplementation(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	now := s.gameplayNow()
	d := now.In(onlineevent.ChinaCalendar).Add(-6 * time.Hour)
	start := time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, onlineevent.ChinaCalendar).AddDate(0, 0, -(int(d.Weekday())-int(time.Thursday)+7)%7)
	season, _ := json.Marshal(map[string]any{"version": 1, "starts": start, "ends": start.AddDate(0, 0, 70), "created_at": now.UTC()})
	g, _ := s.repositoryGroup()
	_ = g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: "server:weekly-attendance-season:v1", Values: map[string]string{"season": string(season)}, UpdatedAt: now})
	account, _, _ := g.Account.Load(nil, "dnf:1")
	state, _ := json.Marshal(map[string]any{"last_observed": now.UTC(), "clears": []uint32{4, 0, 0, 0, 0, 0, 0, 0, 0, 0}, "signed": 0, "catchups": 0, "bonus": 0})
	account.Metadata["event_weekly_attendance_v1:"+start.UTC().Format("20060102T150405Z")] = string(state)
	_ = g.Account.Save(nil, account)
	_ = s.handleGameCommand(a, 1, 1113, make([]byte, 13))
	if n := monthlyMailCount(t, s, "1"); n != 1 {
		t.Fatalf("legal1113 weekly mail=%d want1", n)
	}
}
