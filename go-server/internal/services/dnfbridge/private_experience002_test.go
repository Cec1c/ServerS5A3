package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPrivateExperience002RenewalScope(t *testing.T) {
	a := privateExperience002Archive(t)
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(privateExperienceRetiredDates) != 357 {
		t.Fatal("renewal count")
	}
	for _, r := range privateExperienceRetiredDates {
		d, err := catalog.ResolveItem(r.id)
		if err != nil {
			t.Fatal(err)
		}
		// Relative-duration definitions retain their own earlier instance deadline.
		eligible := !d.DailyDeleteItem && d.UsablePeriodDays == 0 && !d.ExpirationFromUsablePeriod
		if privateExperienceInstanceNeedsRenewal(d, r.previous) != eligible {
			t.Fatalf("renewal %d", r.id)
		}
		if privateExperienceInstanceNeedsRenewal(d, r.previous+1) || !currentGiftBoxHistoryExpiration(d, r.previous, true).IsZero() {
			t.Fatalf("independent/protected date overwritten: %d", r.id)
		}
		bad := d
		bad.PVFPath = "wrong"
		if privateExperienceInstanceNeedsRenewal(bad, r.previous) {
			t.Fatal("path mismatch accepted")
		}
		if currentPVFExpirationUnix(d.ExpirationDate) != 1861826400 {
			t.Fatal("new PVF deadline")
		}
	}
	d, err := catalog.ResolveItem(490008916)
	if err != nil {
		t.Fatal(err)
	}
	if d.ExpirationDate.Year() != 2028 {
		t.Fatal("reported potion still expired")
	}
}

func TestPrivateExperience002OnlineThresholdsAndPreservation(t *testing.T) {
	a := privateExperience002Archive(t)
	loader := &Service{initialEquipmentArchive: a}
	c, ok, err := loader.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
	if err != nil || !ok || c.nativePCRoom == nil {
		t.Fatal(err)
	}
	if c.nativePCRoom.Seconds != [4]uint32{1200, 2400, 4800, 7200} || c.nativePCRoom.Counts != [4]uint32{5, 10, 15, 20} {
		t.Fatalf("catalog %+v", c.nativePCRoom)
	}
	ids := []int64{490009858, 10151892, 490009860, 990027402}
	counts := []int64{1, 1, 5, 1}
	for i, r := range c.nativePCRoom.Daily {
		if r.ItemID != ids[i] || r.Count != counts[i] {
			t.Fatal("reward changed")
		}
	}
	for tier, seconds := range c.nativePCRoom.Seconds {
		t.Run(fmt.Sprint(tier), func(t *testing.T) {
			s, _, _, _, g := onlinePanelFixture(t)
			s.initialEquipmentArchive = a
			s.pvfItemCatalog = nil
			ctx := context.Background()
			g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"})
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, oe.ChinaCalendar)
			owner, _ := oe.NewOwner(g)
			observe := func(end time.Time) {
				if _, err := owner.Observe(ctx, oe.ObserveCommand{AccountID: "a", CharacterID: "1", Definition: c.definition, IntervalFrom: now.Add(-time.Duration(seconds) * time.Second), IntervalTo: end}); err != nil {
					t.Fatal(err)
				}
			}
			observe(now.Add(-time.Second))
			_, err := s.settleCurrentPCRoomTimePoint(ctx, "a", "1", c, now.Add(-time.Second), 1<<tier, -1, nil)
			mail, _, _ := g.Mailbox.Load(ctx, "1")
			if len(mail.Mails) != 0 {
				t.Fatal("early reward")
			}
			observe(now)
			state, err := s.settleCurrentPCRoomTimePoint(ctx, "a", "1", c, now, 1<<tier, -1, nil)
			if err != nil {
				t.Fatal(err)
			}
			if state.DailyMask != (1 << tier) {
				t.Fatal("claim missing")
			}
			if _, err = s.settleCurrentPCRoomTimePoint(ctx, "a", "1", c, now, 1<<tier, -1, nil); err != nil {
				t.Fatal(err)
			}
			mail, _, _ = g.Mailbox.Load(ctx, "1")
			if len(mail.Mails) != 1 {
				t.Fatal("reward or replay")
			}
			for _, m := range mail.Mails {
				if len(m.Attachments) != 1 || m.Attachments[0].ItemID != ids[tier] || m.Attachments[0].Count != counts[tier] {
					t.Fatal("reward attachment")
				}
			}
		})
	}
}
