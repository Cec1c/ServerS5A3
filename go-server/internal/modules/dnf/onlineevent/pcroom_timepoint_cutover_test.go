package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPCRoomCutoverRetainsQualifiedDatesAndBothKindsOfReceipts(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	c, d := pcroomTestCatalog()
	c.Counts[3] = 20
	d.QualificationStageID = d.Stages[3].ID
	legacy := CumulativeDefinition{Daily: d}
	for i, days := range []uint32{5, 10, 15, 25} {
		legacy.Stages = append(legacy.Stages, CumulativeStage{ID: fmt.Sprintf("sum-%d", i+1), RequiredDays: days, Items: []ItemReward{{ItemID: int64(200 + i), Count: 1}}})
	}
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"other": "keep"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 6, 0, 0, 0, ChinaCalendar)
	for day := 0; day < 5; day++ {
		at := start.AddDate(0, 0, day)
		if _, err := o.Observe(ctx, ObserveCommand{AccountID: "a", CharacterID: "1", Definition: d, IntervalFrom: at, IntervalTo: at.Add(3 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		for _, stage := range d.Stages {
			if _, err := o.Claim(ctx, ClaimCommand{AccountID: "a", CharacterID: "1", Definition: d, StageID: stage.ID, ClaimedAt: at.Add(3 * time.Hour), Allocate: cumulativeAllocator(nil)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := start.AddDate(0, 0, 4).Add(3 * time.Hour)
	if _, err := o.ClaimCumulative(ctx, CumulativeClaimCommand{AccountID: "a", CharacterID: "1", Definition: legacy, StageID: "sum-1", ClaimedAt: now, Allocate: cumulativeAllocator(nil)}); err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	cmd := PCRoomTimePointCommand{AccountID: "a", CharacterID: "1", Now: now, ClockDefinition: d, LegacyCumulative: &legacy, DailyClaim: 1, PeriodClaim: -1,
		Deliver: func(repo.MailboxRepository, AttendancePVFItemReward, string) (string, error) {
			deliveries++
			return "mail", nil
		},
	}
	for _, period := range []int{-1, 0, 0} {
		cmd.PeriodClaim = period
		if period >= 0 {
			cmd.DailyClaim = 0
		}
		state, err := SettlePCRoomTimePoint(ctx, g, c, cmd)
		if err != nil || state.Total != 5 || state.DailyMask != 15 || state.PeriodMask != 1 || deliveries != 0 {
			t.Fatalf("cutover state=%+v deliveries=%d err=%v", state, deliveries, err)
		}
	}
	account, _, _ := g.Account.Load(ctx, "a")
	if account.Metadata["other"] != "keep" {
		t.Fatal("unrelated account metadata changed")
	}
	// 新方案第二天只读已提交的在线时长，不要求旧自动发奖路径再生成收据。
	next := start.AddDate(0, 0, 5)
	if _, err := o.Observe(ctx, ObserveCommand{AccountID: "a", CharacterID: "1", Definition: d, IntervalFrom: next, IntervalTo: next.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cmd.Now, cmd.DailyClaim, cmd.PeriodClaim = next.Add(3*time.Hour), 0, -1
	state, err := SettlePCRoomTimePoint(ctx, g, c, cmd)
	if err != nil || state.Total != 6 || state.DailyMask != 0 || state.PeriodMask != 1 {
		t.Fatalf("next day=%+v err=%v", state, err)
	}
}

func TestPCRoomClaimLostCharacterBindingRollsBackMailAndMask(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	c, d := pcroomTestCatalog()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	o, _ := NewOwner(g)
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, ChinaCalendar)
	if _, err := o.Observe(ctx, ObserveCommand{AccountID: "a", CharacterID: "1", Definition: d, IntervalFrom: now.Add(-time.Hour), IntervalTo: now}); err != nil {
		t.Fatal(err)
	}
	valid := true
	cmd := PCRoomTimePointCommand{AccountID: "a", CharacterID: "1", Now: now, ClockDefinition: d, DailyClaim: 1, PeriodClaim: -1, Validate: func() bool { return valid },
		Deliver: func(m repo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
			id, err := repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "test", CompletionKey: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
			valid = false
			return id, err
		},
	}
	if _, err := SettlePCRoomTimePoint(ctx, g, c, cmd); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("stale binding=%v", err)
	}
	mail, _, _ := g.Mailbox.Load(ctx, "1")
	if len(mail.Mails) != 0 {
		t.Fatal("mail committed after binding loss")
	}
	account, _, _ := g.Account.Load(ctx, "a")
	if account.Metadata[PCRoomTimePointMetadataKey] != "" {
		t.Fatal("claim mask committed after binding loss")
	}
}
