package activeness

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/cerashop"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strings"
	"sync"
	"testing"
	"time"
)

const config = `[private activity tasks]
1 1 20 2 1 20 3 1 20 4 1 100 5 1 20 6 100 20 7 1 20 8 1 20
[/private activity tasks]
[private activity rewards]
1 30 500 990027401 1 0 0
2 50 2500 990027402 1 3330 500
3 100 3500 990027403 1 3330 500
[/private activity rewards]`

func fixture(t *testing.T) (repo.Group, *Catalog, Command) {
	t.Helper()
	d, e := pvf.Parse(PVFPath, config)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ParseCatalog(d)
	if e != nil {
		t.Fatal(e)
	}
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}))
	for _, id := range []string{"7", "8"} {
		must(g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Stats: map[string]int64{"gold": 123}}))
		must(g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}))
	}
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	cmd := Command{AccountID: "a", CharacterID: "7", Now: now}
	cmd.Deliver = func(ctx context.Context, m repo.MailboxRepository, r Reward, k string) (string, error) {
		rows := []repo.MailAttachment{{ItemID: int64(r.PotionID), Count: int64(r.PotionCount)}}
		if r.InvitationCount > 0 {
			rows = append(rows, repo.MailAttachment{ItemID: int64(r.InvitationID), Count: int64(r.InvitationCount)})
		}
		return repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Title: "activity", Source: "private_activity", CompletionKey: k, CreatedAt: now, Attachments: rows})
	}
	return g, c, cmd
}
func event(t *testing.T, g repo.Group, c *Catalog, cmd Command, id uint16, n uint32) {
	t.Helper()
	ctx := context.Background()
	e := g.CharacterAssets.WithinCharacterAssets(ctx, cmd.CharacterID, func(cr repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		ch, _, e := cr.Load(ctx, cmd.CharacterID)
		if e != nil {
			return e
		}
		_, e = Apply(&ch, c, id, n, cmd.Now, cmd.Now)
		if e != nil {
			return e
		}
		return repo.SaveCharacterFields(ctx, cr, ch, repo.CharacterFieldStats)
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestActivityEightTasksDailyAndFatigue(t *testing.T) {
	g, c, cmd := fixture(t)
	event(t, g, c, cmd, 6, 99)
	s, e := Settle(context.Background(), g, c, cmd)
	if e != nil || s.Points != 0 || s.Progress[5] != 99 {
		t.Fatalf("%+v %v", s, e)
	}
	event(t, g, c, cmd, 6, 1)
	for id := uint16(1); id <= 8; id++ {
		event(t, g, c, cmd, id, 100)
		event(t, g, c, cmd, id, 100)
	}
	s, e = Settle(context.Background(), g, c, cmd)
	if e != nil || s.Points != 240 {
		t.Fatalf("%+v %v", s, e)
	}
	cmd.CharacterID = "8"
	s, e = Settle(context.Background(), g, c, cmd)
	if e != nil || s.Points != 0 {
		t.Fatalf("another character %v %v", s, e)
	}
}
func TestActivityThreeClaimsConcurrentAtomicWalletMail(t *testing.T) {
	g, c, cmd := fixture(t)
	event(t, g, c, cmd, 4, 1)
	cmd.ExpectedDay = Day(cmd.Now)
	cmd.ExpectedFingerprint = c.Fingerprint
	var wg sync.WaitGroup
	errs := make(chan error, 60)
	for stage := uint16(1); stage <= 3; stage++ {
		for i := 0; i < 20; i++ {
			q := cmd
			q.Claim = stage
			wg.Add(1)
			go func() { defer wg.Done(); _, e := Settle(context.Background(), g, c, q); errs <- e }()
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	a, _, _ := g.Account.Load(context.Background(), "a")
	m, _, _ := g.Mailbox.Load(context.Background(), "7")
	s, e := Settle(context.Background(), g, c, cmd)
	if e != nil || cerashop.WalletBalance(a).HappyTokenCera != 6500 || len(m.Mails) != 3 || s.Claimed != 7 || s.Points != 100 {
		t.Fatalf("wallet=%+v mail=%d state=%+v err=%v", cerashop.WalletBalance(a), len(m.Mails), s, e)
	}
	items := map[int64]int64{}
	for _, mail := range m.Mails {
		for _, at := range mail.Attachments {
			items[at.ItemID] += at.Count
		}
	}
	for id, n := range map[int64]int64{990027401: 1, 990027402: 1, 990027403: 1, 3330: 1000} {
		if items[id] != n {
			t.Fatalf("items %v", items)
		}
	}
}
func TestActivityFailureRollsBackEverything(t *testing.T) {
	for _, mode := range []string{"mail", "session", "overflow", "owner", "points"} {
		t.Run(mode, func(t *testing.T) {
			g, c, cmd := fixture(t)
			if mode != "points" {
				event(t, g, c, cmd, 4, 1)
			}
			cmd.Claim = 1
			cmd.ExpectedDay = Day(cmd.Now)
			cmd.ExpectedFingerprint = c.Fingerprint
			switch mode {
			case "mail":
				deliver := cmd.Deliver
				cmd.Deliver = func(x context.Context, m repo.MailboxRepository, r Reward, k string) (string, error) {
					_, e := deliver(x, m, r, k)
					if e != nil {
						return "", e
					}
					return "", errors.New("after mail save")
				}
			case "session":
				calls := 0
				cmd.Validate = func() error {
					calls++
					if calls == 2 {
						return ErrOwner
					}
					return nil
				}
			case "overflow":
				a, _, _ := g.Account.Load(context.Background(), "a")
				cerashop.SetHappyTokenCeraBalance(&a, cerashop.MaxWalletBalance)
				_ = g.Account.Save(context.Background(), a)
			case "owner":
				cmd.AccountID = "wrong"
			}
			before, _, _ := g.Account.Load(context.Background(), "a")
			if _, e := Settle(context.Background(), g, c, cmd); e == nil {
				t.Fatal("accepted")
			}
			after, _, _ := g.Account.Load(context.Background(), "a")
			m, _, _ := g.Mailbox.Load(context.Background(), "7")
			ch, _, _ := g.Character.Load(context.Background(), "7")
			if cerashop.WalletBalance(before) != cerashop.WalletBalance(after) || len(m.Mails) != 0 || ch.Stats[prefix+"claimed"] != 0 {
				t.Fatal("partial write")
			}
		})
	}
}
func TestActivityResetStaleReplayAndConfigFreeze(t *testing.T) {
	g, c, cmd := fixture(t)
	cmd.Now = time.Date(2026, 10, 27, 21, 59, 59, 0, time.UTC)
	event(t, g, c, cmd, 4, 1)
	ch, _, _ := g.Character.Load(context.Background(), "7")
	old := cmd.Now
	cmd.Now = cmd.Now.Add(time.Second)
	if changed, e := Apply(&ch, c, 4, 1, cmd.Now, old); e != nil || changed || ch.Stats[prefix+"points"] != 0 {
		t.Fatalf("old event changed=%v %v", changed, e)
	}
	event(t, g, c, cmd, 4, 1)
	cmd.Claim = 1
	cmd.ExpectedDay = Day(old)
	cmd.ExpectedFingerprint = c.Fingerprint
	if _, e := Settle(context.Background(), g, c, cmd); !errors.Is(e, ErrStaleDay) {
		t.Fatal(e)
	}
	changed := *c
	changed.Fingerprint++
	cmd.Claim = 0
	if _, e := Settle(context.Background(), g, &changed, cmd); !errors.Is(e, ErrCatalogChanged) {
		t.Fatal(e)
	}
}
func TestActivityRejectsMalformedPVF(t *testing.T) {
	for _, x := range []string{strings.Replace(config, "6 100 20", "6 0 20", 1), strings.Replace(config, "2 50 2500", "2 20 2500", 1), config + "\n[private activity tasks]\n1 1 1\n[/private activity tasks]"} {
		d, e := pvf.Parse(PVFPath, x)
		if e != nil {
			continue
		}
		if _, e = ParseCatalog(d); e == nil {
			t.Fatal("accepted invalid PVF")
		}
	}
}
