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

func epicTestCampaign(t *testing.T, g repo.Group) EpicCampaign {
	t.Helper()
	c, e := ActivateEpicCampaign(nil, g, "a", "1", time.Date(2026, 10, 10, 10, 0, 0, 0, ChinaCalendar), func() bool { return true })
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func epicTestClaim(t *testing.T) (repo.Group, *EpicCatalog, EpicClaimCommand) {
	t.Helper()
	g := weeklyTestRepos(t)
	c := epicTestCampaign(t, g)
	ch, _, _ := g.Character.Load(nil, "1")
	ch.Level = 90
	if e := AccumulateEpicInvitations(&ch, c, c.CreatedAt, 18000); e != nil {
		t.Fatal(e)
	}
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	cat := &EpicCatalog{Thresholds: [3]uint32{1800, 9000, 18000}, Rewards: [3]AttendancePVFItemReward{{ItemID: 42, Count: 1}, {ItemID: 43, Count: 1}, {ItemID: 44, Count: 1}}}
	cmd := EpicClaimCommand{AccountID: "a", CharacterID: "1", Campaign: c, Now: c.CreatedAt, Validate: func() bool { return true }}
	cmd.Deliver = func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(nil, m, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: key, CreatedAt: cmd.Now, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}
	return g, cat, cmd
}
func TestEpicCampaignGlobalAnchorNoRestartRolloverOrClockReset(t *testing.T) {
	g := weeklyTestRepos(t)
	first := epicTestCampaign(t, g)
	want := time.Date(2026, 10, 10, 6, 0, 0, 0, ChinaCalendar)
	if !first.Starts.Equal(want) || !first.Ends.Equal(want.AddDate(0, 0, 28)) {
		t.Fatal(first)
	}
	for _, after := range []time.Time{first.CreatedAt.Add(time.Hour), first.CreatedAt.AddDate(0, 1, 0), first.CreatedAt.AddDate(0, 0, -1)} {
		c, e := ActivateEpicCampaign(nil, g, "b", "3", after, func() bool { return true })
		if e != nil || c.Key() != first.Key() || !c.CreatedAt.Equal(first.CreatedAt) {
			t.Fatal(c, e)
		}
	}
	if first.Active(first.CreatedAt.Add(-time.Second)) || first.Active(first.Ends) || !first.Active(first.Ends.Add(-time.Nanosecond)) {
		t.Fatal("campaign boundary")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := ActivateEpicCampaign(nil, g, "a", "2", first.CreatedAt, func() bool { return true })
			if e != nil || c.Key() != first.Key() || !c.CreatedAt.Equal(first.CreatedAt) {
				t.Error(c, e)
			}
		}()
	}
	wg.Wait()
	if _, e := ActivateEpicCampaign(nil, g, "b", "1", first.CreatedAt, func() bool { return true }); e == nil {
		t.Fatal("foreign owner")
	}
}
func TestEpicClaimConcurrentIndexReplayAndCharacterScope(t *testing.T) {
	g, cat, cmd := epicTestClaim(t)
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := cmd
			c.Index = uint32(i % 3)
			if _, e := ClaimEpicReward(nil, g, cat, c); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	ch, _, _ := g.Character.Load(nil, "1")
	state, e := EpicCharacterSnapshot(ch, cmd.Campaign, cmd.Now)
	m, _, _ := g.Mailbox.Load(nil, "1")
	if e != nil || state.Claimed != 7 || state.Invitations != 18000 || len(m.Mails) != 3 {
		t.Fatal(state, e, len(m.Mails))
	}
	other, _, _ := g.Character.Load(nil, "2")
	s, e := EpicCharacterSnapshot(other, cmd.Campaign, cmd.Now)
	if e != nil || s.Invitations != 0 || s.Claimed != 0 {
		t.Fatal("shared counter", s, e)
	}
}

type epicFaultMailbox struct {
	repo.MailboxAssetUnitOfWork
	fault error
}

func (f epicFaultMailbox) WithinMailboxAssets(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return f.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, func(c repo.CharacterRepository, v repo.InventoryRepository, m repo.MailboxRepository) error {
		if e := fn(c, v, m); e != nil {
			return e
		}
		return f.fault
	})
}
func TestEpicClaimLateRollbackThenRetry(t *testing.T) {
	g, cat, cmd := epicTestClaim(t)
	under := g.MailboxAssets
	fault := errors.New("after both writes before physical commit")
	g.MailboxAssets = epicFaultMailbox{under, fault}
	if _, e := ClaimEpicReward(nil, g, cat, cmd); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	ch, _, _ := g.Character.Load(nil, "1")
	s, _ := EpicCharacterSnapshot(ch, cmd.Campaign, cmd.Now)
	m, _, _ := g.Mailbox.Load(nil, "1")
	if s.Claimed != 0 || len(m.Mails) != 0 {
		t.Fatal(s, m)
	}
	g.MailboxAssets = under
	if _, e := ClaimEpicReward(nil, g, cat, cmd); e != nil {
		t.Fatal(e)
	}
	if _, e := ClaimEpicReward(nil, g, cat, cmd); e != nil {
		t.Fatal(e)
	}
	m, _, _ = g.Mailbox.Load(nil, "1")
	if len(m.Mails) != 1 {
		t.Fatal(len(m.Mails))
	}
}
func TestEpicClaimInvalidOwnersCampaignIndexThresholdClock(t *testing.T) {
	for _, name := range []string{"account", "character", "index", "expired", "rollback", "foreigncampaign", "threshold", "generation", "panic"} {
		t.Run(name, func(t *testing.T) {
			g, cat, c := epicTestClaim(t)
			switch name {
			case "account":
				c.AccountID = "b"
			case "character":
				c.CharacterID = "2"
			case "index":
				c.Index = 3
			case "expired":
				c.Now = c.Campaign.Ends
			case "rollback":
				c.Now = c.Now.Add(-time.Second)
			case "foreigncampaign":
				c.Campaign.Starts = c.Campaign.Starts.AddDate(0, 0, 1)
				c.Campaign.Ends = c.Campaign.Ends.AddDate(0, 0, 1)
				c.Campaign.CreatedAt = c.Campaign.CreatedAt.AddDate(0, 0, 1)
				c.Now = c.Campaign.CreatedAt
			case "threshold":
				ch, _, _ := g.Character.Load(nil, "1")
				ch.Stats[epicStatKey(c.Campaign, "invitations")] = 1799
				g.Character.Save(nil, ch)
			case "generation":
				calls := 0
				c.Validate = func() bool { calls++; return calls < 2 }
			case "panic":
				c.Deliver = func(repo.MailboxRepository, AttendancePVFItemReward, string) (string, error) { panic("fault") }
			}
			if _, e := ClaimEpicReward(nil, g, cat, c); e == nil {
				t.Fatal("invalid claim accepted")
			}
			m, _, _ := g.Mailbox.Load(nil, "1")
			if len(m.Mails) != 0 {
				t.Fatal("leaked mail")
			}
		})
	}
}
func TestEpicActual369dCatalog(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF not supplied")
	}
	a, e := pvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}

	c, e := LoadEpicCatalog(nil, a)
	if e != nil {
		t.Fatal(e)
	}
	if c.Rewards[0].ItemID != 490010224 || c.Rewards[1].ItemID != 490010218 || c.Rewards[2].ItemID != 490010223 {
		t.Fatal(c)
	}
}
