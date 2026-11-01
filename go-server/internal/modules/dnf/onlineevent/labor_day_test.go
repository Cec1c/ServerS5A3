package onlineevent

import (
	"context"
	"os"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealLaborDayPVFCatalog(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadLaborDayCatalog(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{10005051, 10005052, 10005053} {
		if c.Login[i].ItemID != id || c.Login[i].DefinitionPath == "" {
			t.Fatalf("login[%d]=%+v", i, c.Login[i])
		}
	}
	for i, id := range []int64{490001502, 490001503, 490001504} {
		if c.Online[i].ItemID != id || c.Online[i].DefinitionPath == "" {
			t.Fatalf("online[%d]=%+v", i, c.Online[i])
		}
	}
}

func TestLaborDayOnlineTierMatchesNativeTable(t *testing.T) {
	for paid, want := range map[uint32]int{0: 0, 3: 0, 4: 1, 8: 1, 9: 2, 10: 2} {
		if got := LaborDayOnlineTier(paid); got != want {
			t.Fatalf("paid=%d tier=%d want=%d", paid, got, want)
		}
	}
}

func TestSettleLaborDayLoginAndTenMinuteDistribution(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 16, 6, 0, 0, 0, ChinaCalendar)
	c := &LaborDayCatalog{Policy: LaborDayPolicy{Starts: start, Ends: start.Add(LaborDayDuration)}}
	for i := range c.Login {
		c.Login[i] = AttendancePVFItemReward{ItemID: int64(499 + i), Count: 1}
		c.Online[i] = AttendancePVFItemReward{ItemID: int64(502 + i), Count: 1}
	}
	var delivered []int64
	deliver := func(_ dnfrepo.MailboxRepository, r AttendancePVFItemReward, _ string) (string, error) {
		delivered = append(delivered, r.ItemID)
		return "mail", nil
	}
	cmd := LaborDayCommand{AccountID: "a", CharacterID: "1", Now: start, Login: true, Deliver: deliver}
	s, err := SettleLaborDay(ctx, repos, c, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if s.LoginDays != 1 || len(delivered) != 1 || delivered[0] != 499 {
		t.Fatalf("login snapshot=%+v delivered=%v", s, delivered)
	}
	cmd.Login = false
	cmd.From = start
	cmd.To = start.Add(100 * time.Minute)
	cmd.Now = cmd.To
	s, err = SettleLaborDay(ctx, repos, c, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if s.TodayPaid != 10 || s.TodaySeconds != 6000 || len(delivered) != 11 {
		t.Fatalf("online snapshot=%+v delivered=%v", s, delivered)
	}
	for i, want := range []int64{502, 502, 502, 502, 503, 503, 503, 503, 503, 504} {
		if delivered[i+1] != want {
			t.Fatalf("award %d=%d want=%d", i, delivered[i+1], want)
		}
	}
	cmd.From, cmd.To, cmd.Now = cmd.To, cmd.To.Add(10*time.Minute), cmd.To.Add(10*time.Minute)
	s, err = SettleLaborDay(ctx, repos, c, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if s.TodayPaid != 10 || len(delivered) != 11 {
		t.Fatalf("daily cap snapshot=%+v delivered=%v", s, delivered)
	}
}
