package onlineevent

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sync"
	"testing"
	"time"
)

func TestMonthlyActual369dCatalogIndependent(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual369d resource audit requires PVF path")
	}
	archive, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Snapshot().Checksum != "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06" {
		t.Fatal("wrong archive")
	}
	c, err := LoadMonthlyCatalog(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if c.Thresholds != [3]uint32{7, 14, 21} {
		t.Fatalf("thresholds=%v", c.Thresholds)
	}
	for i, r := range c.Daily {
		if r.ItemID != 490010051+int64(i%7) || r.Count != 1 || r.DefinitionPath == "" {
			t.Fatalf("daily%d=%+v", i, r)
		}
	}
	for i, r := range c.Bonus {
		if r.ItemID != 490010058+int64(i) || r.Count != 1 || r.DefinitionPath == "" {
			t.Fatalf("bonus%d=%+v", i, r)
		}
	}
}
func TestMonthlyConcurrentOwnedCharactersCommitOneRewardIndependent(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	a := repo.AccountRecord{AccountID: "a"}
	now := time.Date(2026, 10, 10, 6, 0, 0, 0, ChinaCalendar)
	for i := 0; i < 2; i++ {
		if err := RecordMonthlyClear(&a, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"19", "20"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	c := &MonthlyCatalog{Thresholds: [3]uint32{7, 14, 21}}
	for i := range c.Daily {
		c.Daily[i] = AttendancePVFItemReward{ItemID: 490010051 + int64(i%7), Count: 1}
	}
	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		id := []string{"19", "20"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := SettleMonthlyAttendance(ctx, g, c, MonthlyCommand{AccountID: "a", CharacterID: id, Now: now, ClaimBonus: true, Deliver: func(m repo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
				return repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: id, Source: key, Attachments: []repo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
			}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, id := range []string{"19", "20"} {
		m, _, err := g.Mailbox.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		count += len(m.Mails)
	}
	if count != 1 {
		t.Fatalf("64 competing claims produced%d mails", count)
	}
}
