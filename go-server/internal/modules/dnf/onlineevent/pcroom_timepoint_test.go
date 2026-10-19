package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPCRoomTimePointPVFCatalog(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadPCRoomTimePointCatalog(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if c.Seconds != [4]uint32{1800, 3600, 7200, 10800} || c.Counts != [4]uint32{5, 10, 15, 20} {
		t.Fatalf("catalog=%+v", c)
	}
	for i, id := range []int64{490009858, 490009859, 490009860, 490009861} {
		if c.Daily[i].ItemID != id || c.Daily[i].DefinitionPath == "" {
			t.Fatalf("daily[%d]=%+v", i, c.Daily[i])
		}
	}
	for i, id := range []int64{490009862, 490009865, 490009866, 490009867} {
		if c.Period[i].ItemID != id || c.Period[i].DefinitionPath == "" {
			t.Fatalf("period[%d]=%+v", i, c.Period[i])
		}
	}
}

func pcroomTestCatalog() (*PCRoomTimePointCatalog, Definition) {
	c := &PCRoomTimePointCatalog{Seconds: [4]uint32{1800, 3600, 7200, 10800}, Counts: [4]uint32{5, 10, 15, 25}}
	d := Definition{ID: "2347"}
	for i := range c.Daily {
		c.Daily[i] = AttendancePVFItemReward{ItemID: int64(100 + i), Count: 1}
		c.Period[i] = AttendancePVFItemReward{ItemID: int64(200 + i), Count: 1}
		d.Stages = append(d.Stages, Stage{ID: fmt.Sprintf("stage-%d", i+1), RequiredSeconds: uint64(c.Seconds[i]), Items: []ItemReward{{ItemID: int64(300 + i), Count: 1}}})
	}
	return c, d
}

func TestPCRoomTimePointClockClaimsCrossDayAndRollback(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c, d := pcroomTestCatalog()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 16, 6, 0, 0, 0, ChinaCalendar)
	cmd := PCRoomTimePointCommand{AccountID: "a", CharacterID: "1", Now: start, ClockDefinition: d, PeriodClaim: -1}
	fail := false
	cmd.Deliver = func(m dnfrepo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		id, err := dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: "pcroom-test", CompletionKey: key, CreatedAt: cmd.Now, Attachments: []dnfrepo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
		if fail {
			return "", errors.New("failure after mail write")
		}
		return id, err
	}
	observe := func(from, to time.Time) {
		t.Helper()
		if _, err := owner.Observe(ctx, ObserveCommand{AccountID: "a", CharacterID: "1", Definition: d, IntervalFrom: from, IntervalTo: to}); err != nil {
			t.Fatal(err)
		}
		cmd.Now = to
	}
	settle := func() PCRoomTimePointSnapshot {
		t.Helper()
		s, err := SettlePCRoomTimePoint(ctx, repos, c, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cmd.DailyClaim = 1
	observe(start, start.Add(1799*time.Second))
	if s := settle(); s.DailyMask != 0 || len(s.MailIDs) != 0 {
		t.Fatalf("early claim=%+v", s)
	}
	observe(start, start.Add(1800*time.Second))
	fail = true
	if _, err := SettlePCRoomTimePoint(ctx, repos, c, cmd); err == nil {
		t.Fatal("mail failure committed")
	}
	mb, _, _ := repos.Mailbox.Load(ctx, "1")
	if len(mb.Mails) != 0 {
		t.Fatal("failed mail leaked")
	}
	fail = false
	if s := settle(); s.DailyMask != 1 || len(s.MailIDs) != 1 {
		t.Fatalf("claim=%+v", s)
	}
	cmd.CharacterID = "2"
	if s := settle(); s.DailyMask != 1 || len(s.MailIDs) != 0 {
		t.Fatalf("cross-character replay=%+v", s)
	}
	cmd.DailyClaim = 0
	observe(start, start.Add(3*time.Hour))
	if s := settle(); s.Total != 1 || s.DailyMask != 1 {
		t.Fatalf("full day independent of claim=%+v", s)
	}
	if s := settle(); s.Total != 1 {
		t.Fatalf("double count=%+v", s)
	}
	for day := 1; day < 25; day++ {
		at := start.AddDate(0, 0, day)
		cmd.Now = at
		if s := settle(); s.Seconds != 0 || s.DailyMask != 0 || s.Total != uint32(day) {
			t.Fatalf("reset=%+v day=%d", s, day)
		}
		observe(at, at.Add(3*time.Hour))
		s := settle()
		if s.Total != uint32(day+1) {
			t.Fatalf("count=%+v day=%d", s, day)
		}
		for i, n := range c.Counts {
			cmd.PeriodClaim = i
			s = settle()
			if s.Total < n && s.PeriodMask&(1<<i) != 0 {
				t.Fatalf("locked period=%+v", s)
			}
			if s.Total == n && len(s.MailIDs) != 1 {
				t.Fatalf("threshold %d state=%+v", n, s)
			}
			if again := settle(); len(again.MailIDs) != 0 {
				t.Fatal("period replay mailed")
			}
		}
		cmd.PeriodClaim = -1
	}
	if s := settle(); s.Total != 25 || s.PeriodMask != 15 {
		t.Fatalf("final=%+v", s)
	}
	cmd.Now = start // stale requests never clear the current ledger.
	if _, err := SettlePCRoomTimePoint(ctx, repos, c, cmd); !errors.Is(err, ErrObservationStale) {
		t.Fatalf("stale=%v", err)
	}
	cmd.Now = start.AddDate(0, 0, 24).Add(3 * time.Hour)
	cmd.AccountID = "wrong"
	if _, err := SettlePCRoomTimePoint(ctx, repos, c, cmd); !errors.Is(err, dnfrepo.ErrMailboxAssetTransactionUnavailable) {
		t.Fatalf("ownership=%v", err)
	}
}

func TestPCRoomTimePointConcurrentClaimAndLegacyCutover(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	c, d := pcroomTestCatalog()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, ChinaCalendar)
	legacy := newState(d.ID)
	legacy.resetTo("2026-09-16")
	legacy.creditInterval(0, 10800)
	legacy.Claims["stage-1"] = claimReceipt{StageID: "stage-1", CharacterID: "1", ClaimedAt: now, Items: []CommittedItem{{SlotKey: "0:9", ItemID: 300, Delta: 1, PostCount: 1}}}
	raw, err := encodeState(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{metadataKey(d.ID): raw}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	cmd := PCRoomTimePointCommand{AccountID: "a", CharacterID: "1", Now: now, ClockDefinition: d, DailyClaim: 1, PeriodClaim: -1}
	cmd.Deliver = func(m dnfrepo.MailboxRepository, r AttendancePVFItemReward, key string) (string, error) {
		return dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: "1", Source: "pcroom-test", CompletionKey: key, CreatedAt: now, Attachments: []dnfrepo.MailAttachment{{ItemID: r.ItemID, Count: r.Count}}})
	}
	s, err := SettlePCRoomTimePoint(ctx, repos, c, cmd)
	if err != nil || s.DailyMask != 1 || s.Total != 1 || len(s.MailIDs) != 0 {
		t.Fatalf("cutover=%+v %v", s, err)
	}
	cmd.DailyClaim = 2
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := SettlePCRoomTimePoint(ctx, repos, c, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	mb, _, err := repos.Mailbox.Load(ctx, "1")
	if err != nil || len(mb.Mails) != 1 {
		t.Fatalf("concurrent mail count=%d err=%v", len(mb.Mails), err)
	}
}
