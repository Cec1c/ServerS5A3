package onlineevent

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sync"
	"testing"
	"time"
)

func ordinaryFixture(t *testing.T) (repo.Group, *OrdinaryCatalog, time.Time) {
	g := weeklyTestRepos(t)
	for id, level := range map[string]int{"1": 17, "2": 90, "3": 90} {
		ch, _, _ := g.Character.Load(nil, id)
		ch.Level = level
		ch.Stats = map[string]int64{"keep": 7}
		if e := g.Character.Save(nil, ch); e != nil {
			t.Fatal(e)
		}
	}
	c := &OrdinaryCatalog{MinimumLevel: 17}
	for i := range c.Rewards {
		c.Rewards[i] = AttendancePVFItemReward{ItemID: 214, Count: 1}
	}
	return g, c, time.Date(2026, 10, 31, 6, 0, 0, 0, ChinaCalendar)
}
func ordinaryCmd(now time.Time, char string) OrdinaryCommand {
	return OrdinaryCommand{AccountID: "a", CharacterID: char, Now: now, RegisterDay: true, Deliver: func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(context.Background(), m, repo.SystemMailDelivery{RecipientCharacterID: char, Source: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}}
}
func TestOrdinaryDayChinaSixBoundaryAccountSharingNoMissingDayBackfill(t *testing.T) {
	g, c, now := ordinaryFixture(t)
	cmd := ordinaryCmd(now.Add(-time.Second), "1")
	state, e := SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || state.Days != 1 || !state.CanClaim {
		t.Fatalf("initial%+v %v", state, e)
	}
	cmd = ordinaryCmd(now, "2")
	state, e = SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || state.Days != 2 {
		t.Fatalf("six%+v %v", state, e)
	}
	cmd = ordinaryCmd(now.AddDate(0, 0, 3), "1")
	cmd.Claim = true
	state, e = SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || state.Days != 3 || state.Claimed != 3 || len(state.MailIDs) != 3 {
		t.Fatalf("missed days backfilled%+v %v", state, e)
	}
	state, e = SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || len(state.MailIDs) != 0 {
		t.Fatal("replay rewards")
	}
	a, _, _ := g.Account.Load(nil, "a")
	if a.Metadata["keep"] != "yes" {
		t.Fatal("metadata clobbered")
	}
	stored, e := loadOrdinaryState(a)
	if e != nil || stored.Days[0].Date != "2026-10-30" || stored.Days[1].Date != "2026-10-31" || stored.Days[2].Date != "2026-11-03" {
		t.Fatalf("dates%+v %v", stored, e)
	}
}
func TestOrdinaryLevelSeventeenPhysicalSnapshotForeignAndMax28(t *testing.T) {
	g, c, now := ordinaryFixture(t)
	ch, _, _ := g.Character.Load(nil, "1")
	ch.Level = 16
	_ = g.Character.Save(nil, ch)
	cmd := ordinaryCmd(now, "1")
	cmd.Claim = true
	state, e := SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || state.Days != 0 || state.CanClaim || len(state.MailIDs) > 0 {
		t.Fatalf("level16%+v %v", state, e)
	}
	cmd.CharacterID = "3"
	if _, e = SettleOrdinaryAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("foreign char")
	}
	ch.Level = 17
	_ = g.Character.Save(nil, ch)
	cmd = ordinaryCmd(now, "1")
	cmd.Claim = true
	for day := 0; day < 30; day++ {
		cmd.Now = now.AddDate(0, 0, day)
		state, e = SettleOrdinaryAttendance(nil, g, c, cmd)
		if e != nil || state.Days != uint32(min(day+1, 28)) {
			t.Fatalf("day%d%+v %v", day, state, e)
		}
	}
	m, _, _ := g.Mailbox.Load(nil, "1")
	if len(m.Mails) != 28 {
		t.Fatal("max reward count")
	}
	ch, _, _ = g.Character.Load(nil, "1")
	if ch.Level != 17 || ch.Stats["keep"] != 7 {
		t.Fatal("character snapshot became writable")
	}
}
func TestOrdinaryStagedMailMetadataOwnerFailureAndRollbackClock(t *testing.T) {
	g, c, now := ordinaryFixture(t)
	cmd := ordinaryCmd(now, "1")
	cmd.Claim = true
	original := cmd.Deliver
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		_, e := original(m, r, key)
		if e != nil {
			return "", e
		}
		return "", errors.New("postmail fault")
	}
	if _, e := SettleOrdinaryAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("mail error accepted")
	}
	a, _, _ := g.Account.Load(nil, "a")
	m, _, _ := g.Mailbox.Load(nil, "1")
	if a.Metadata[OrdinaryMetadataKey] != "" || len(m.Mails) != 0 {
		t.Fatal("mail fault leaked")
	}
	cmd.Deliver = original
	calls := 0
	cmd.Validate = func() bool { calls++; return calls < 3 }
	if _, e := SettleOrdinaryAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("postmetadata invalidation committed")
	}
	a, _, _ = g.Account.Load(nil, "a")
	m, _, _ = g.Mailbox.Load(nil, "1")
	if a.Metadata[OrdinaryMetadataKey] != "" || len(m.Mails) != 0 {
		t.Fatal("metadata fault leaked")
	}
	cmd.Validate = nil
	state, e := SettleOrdinaryAttendance(nil, g, c, cmd)
	if e != nil || state.Days != 1 || state.Claimed != 1 {
		t.Fatalf("retry%+v %v", state, e)
	}
	cmd.Now = now.Add(-time.Second)
	if _, e = SettleOrdinaryAttendance(nil, g, c, cmd); e == nil {
		t.Fatal("rollback accepted")
	}
}
func TestOrdinaryConcurrentDualRoleActualDayOnlyOneMail(t *testing.T) {
	g, c, now := ordinaryFixture(t)
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
			cmd := ordinaryCmd(now, id)
			cmd.Claim = true
			_, e := SettleOrdinaryAttendance(nil, g, c, cmd)
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
		t.Fatal("duplicate")
	}
}
func TestOrdinaryActual369dTwentyEightCatalog(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual369d")
	}
	archive, e := pvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if archive.Snapshot().Checksum != "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06" {
		t.Fatal("actual PVF mismatch")
	}
	c, e := LoadOrdinaryCatalog(nil, archive)
	if e != nil || c.MinimumLevel != 17 {
		t.Fatalf("catalog%+v %v", c, e)
	}
	for _, r := range c.Rewards {
		if r.ItemID != 490010214 || r.Count != 1 || r.DefinitionPath == "" {
			t.Fatalf("actual reward%+v", r)
		}
	}
}
