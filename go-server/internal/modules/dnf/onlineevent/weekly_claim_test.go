package onlineevent

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sync"
	"testing"
	"time"
)

func weeklyFixture(t *testing.T) (repo.Group, *WeeklyCatalog, WeeklySeason, time.Time) {
	g := weeklyTestRepos(t)
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, ChinaCalendar)
	season, e := ActivateWeeklySeason(nil, g, "a", "1", now, func() bool { return true })
	if e != nil {
		t.Fatal(e)
	}
	c := &WeeklyCatalog{RequiredClears: 4, CatchupClears: 20, CatchupLimit: 2, Thresholds: [3]uint32{3, 6, 9}}
	for i := range c.Rewards {
		c.Rewards[i] = AttendancePVFItemReward{ItemID: int64(970 + i), Count: 1}
	}
	for i := range c.Bonus {
		c.Bonus[i] = AttendancePVFItemReward{ItemID: int64(980 + i), Count: 1}
	}
	return g, c, season, now
}
func weeklySeed(t *testing.T, g repo.Group, c *WeeklyCatalog, s WeeklySeason, now time.Time, n int) {
	a, _, e := g.Account.Load(nil, "a")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < n; i++ {
		if e := RecordWeeklyClear(&a, c, s, now); e != nil {
			t.Fatal(e)
		}
	}
	if e := g.Account.Save(nil, a); e != nil {
		t.Fatal(e)
	}
}
func weeklyCmd(s WeeklySeason, now time.Time, char string) WeeklyCommand {
	cmd := WeeklyCommand{AccountID: "a", CharacterID: char, Season: s, Now: now, Claim: true, ClaimBonus: true}
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(context.Background(), m, repo.SystemMailDelivery{RecipientCharacterID: char, Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}
	return cmd
}
func TestWeeklyTenWeeksBonusMailAndCrossRoleReplay(t *testing.T) {
	g, c, s, now := weeklyFixture(t)
	for week := 0; week < 10; week++ {
		at := now.AddDate(0, 0, week*7)
		weeklySeed(t, g, c, s, at, 4)
		cmd := weeklyCmd(s, at, "1")
		state, e := SettleWeeklyAttendance(nil, g, c, cmd)
		want := 1
		if week == 2 || week == 5 || week == 8 {
			want++
		}
		if e != nil || state.Total != uint32(week+1) || len(state.MailIDs) != want || state.Clears != 0 {
			t.Fatalf("week%d %+v %v", week, state, e)
		}
		replay, e := SettleWeeklyAttendance(nil, g, c, weeklyCmd(s, at, "2"))
		if e != nil || len(replay.MailIDs) != 0 {
			t.Fatalf("replay %+v %v", replay, e)
		}
	}
	m, _, _ := g.Mailbox.Load(nil, "1")
	if len(m.Mails) != 13 {
		t.Fatal(len(m.Mails))
	}
	m, _, _ = g.Mailbox.Load(nil, "2")
	if len(m.Mails) != 0 {
		t.Fatal("shared reward duplicated")
	}
	cmd := weeklyCmd(s, now.AddDate(0, 0, 63), "3")
	if _, e := SettleWeeklyAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("foreign role accepted")
	}
	closed, e := SettleWeeklyAttendance(nil, g, c, weeklyCmd(s, s.Ends, "1"))
	if e != nil || closed.Week != 11 || closed.CanSign || closed.CanBonus || len(closed.MailIDs) != 0 {
		t.Fatalf("closed%+v %v", closed, e)
	}
}
func TestWeeklyCatchupCostLimitAndClockRollback(t *testing.T) {
	g, c, s, now := weeklyFixture(t)
	at := now.AddDate(0, 0, 21)
	weeklySeed(t, g, c, s, at, 44)
	cmd := weeklyCmd(s, at, "1")
	for i := 0; i < 3; i++ {
		state, e := SettleWeeklyAttendance(nil, g, c, cmd)
		if e != nil || state.Total != uint32(i+1) || state.Clears != []uint32{40, 20, 0}[i] {
			t.Fatalf("claim%d %+v %v", i, state, e)
		}
	}
	state, e := SettleWeeklyAttendance(nil, g, c, cmd)
	if e != nil || state.Catchups != 2 || state.CanCatchup || len(state.MailIDs) != 0 {
		t.Fatalf("limit %+v %v", state, e)
	}
	cmd.Now = at.Add(-time.Second)
	if _, e = SettleWeeklyAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("rollback claim accepted")
	}
	a, _, _ := g.Account.Load(nil, "a")
	before := a.Metadata[s.Key()]
	if e := RecordWeeklyClear(&a, c, s, cmd.Now); e != nil || a.Metadata[s.Key()] != before {
		t.Fatal("clock rollback rewrote state")
	}
}
func TestWeeklyMailFailureAndInvalidatedOwnerRollback(t *testing.T) {
	g, c, s, now := weeklyFixture(t)
	weeklySeed(t, g, c, s, now, 4)
	a, _, _ := g.Account.Load(nil, "a")
	before := a.Metadata[s.Key()]
	cmd := weeklyCmd(s, now, "1")
	deliver := cmd.Deliver
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		_, e := deliver(m, r, key)
		if e != nil {
			return "", e
		}
		return "", errors.New("postmail failure")
	}
	if _, e := SettleWeeklyAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("mail error accepted")
	}
	a, _, _ = g.Account.Load(nil, "a")
	m, _, _ := g.Mailbox.Load(nil, "1")
	if a.Metadata[s.Key()] != before || len(m.Mails) != 0 {
		t.Fatal("failure leaked")
	}
	cmd.Deliver = deliver
	calls := 0
	cmd.Validate = func() bool { calls++; return calls < 3 }
	if _, e := SettleWeeklyAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("postaccount owner invalidation accepted")
	}
	a, _, _ = g.Account.Load(nil, "a")
	m, _, _ = g.Mailbox.Load(nil, "1")
	if a.Metadata[s.Key()] != before || len(m.Mails) != 0 {
		t.Fatal("invalidated transaction leaked")
	}
	cmd.Validate = nil
	if state, e := SettleWeeklyAttendance(nil, g, c, cmd); e != nil || len(state.MailIDs) != 1 {
		t.Fatalf("retry %+v %v", state, e)
	}
}
func TestWeeklyConcurrentDualRoleClaimOnlyOneMail(t *testing.T) {
	g, c, s, now := weeklyFixture(t)
	weeklySeed(t, g, c, s, now, 4)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "1"
			if i%2 == 1 {
				id = "2"
			}
			_, e := SettleWeeklyAttendance(nil, g, c, weeklyCmd(s, now, id))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	a, _, _ := g.Mailbox.Load(nil, "1")
	b, _, _ := g.Mailbox.Load(nil, "2")
	if len(a.Mails)+len(b.Mails) != 1 {
		t.Fatal("duplicate concurrent reward")
	}
}
