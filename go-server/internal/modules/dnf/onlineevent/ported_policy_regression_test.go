package onlineevent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPortedPolicySevenDaysAndLongTerm(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	p, err := LoadPortedActivityPolicy(ctx, r.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if p.ReturneeOfflineDays != 7 || p.ReturneeDurationDays != 30 || !p.LuckyAllProfessions {
		t.Fatalf("默认策略%+v", p)
	}
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, ChinaCalendar)
	for _, w := range []ActivityWindow{p.LevelAiming, p.LaborDay, p.LuckyCharacter} {
		if !w.ActiveAt(now.AddDate(5, 0, 0)) {
			t.Fatal("长期活动自动过期")
		}
	}
	c := &ReturneeCatalog{}
	for _, delta := range []time.Duration{7*24*time.Hour - time.Nanosecond, 7 * 24 * time.Hour} {
		s := ReturneeState{LastSeen: now.Add(-delta)}
		s.Observe(c, "1", ReturneeObservation{}, now, true)
		if s.ActiveAt(now) != (delta >= 7*24*time.Hour) {
			t.Fatalf("离线%s资格错误", delta)
		}
	}
	p.ReturneeOfflineDays = 14
	p.ReturneeDurationDays = 45
	p.LevelAiming = ActivityWindow{Starts: now, Ends: now.Add(time.Hour)}
	raw, _ := json.Marshal(p)
	if err := r.Settings.Save(ctx, repo.SettingsRecord{Scope: PortedActivityPolicyScope, Values: map[string]string{"state": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPortedActivityPolicy(ctx, r.Settings)
	if err != nil || loaded.ReturneeOfflineDays != 14 || loaded.LevelAiming.ActiveAt(now.Add(time.Hour)) {
		t.Fatalf("配置读取%+v %v", loaded, err)
	}
}

func TestPortedLaborDayLongTermRetentionAndReplays(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	start := time.Date(2026, 10, 29, 6, 0, 0, 0, ChinaCalendar)
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "untouched"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1", Level: 1}); err != nil {
		t.Fatal(err)
	}
	c := &LaborDayCatalog{Policy: LaborDayPolicy{Starts: start}}
	for i := range c.Login {
		c.Login[i] = AttendancePVFItemReward{ItemID: int64(100 + i), Count: 1}
		c.Online[i] = AttendancePVFItemReward{ItemID: int64(200 + i), Count: 1}
	}
	deliver := func(box repo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(ctx, box, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "test", CompletionKey: key, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	var first LaborDayCommand
	for i := 0; i < 400; i++ {
		from := start.AddDate(0, 0, i)
		cmd := LaborDayCommand{AccountID: "a", CharacterID: "1", Now: from.Add(10 * time.Minute), From: from, To: from.Add(10 * time.Minute), Login: true, Deliver: deliver}
		if i == 0 {
			first = cmd
		}
		for j := 0; j < 2; j++ {
			s, err := SettleLaborDay(ctx, r, c, cmd)
			if err != nil || s.TodayPaid != 1 || s.LoginDays != uint32(min(i+1, 3)) {
				t.Fatalf("第%d天第%d次 %+v %v", i, j, s, err)
			}
		}
	}
	a, _, _ := r.Account.Load(ctx, "a")
	var state laborDayState
	if err := json.Unmarshal([]byte(a.Metadata[LaborDayMetadataKey]), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Days) > 31 || a.Metadata["keep"] != "untouched" {
		t.Fatalf("长期状态无界或覆盖其它元数据：%d", len(state.Days))
	}
	before, _, _ := r.Mailbox.Load(ctx, "1")
	if len(before.Mails) != 403 {
		t.Fatalf("重复或漏发邮件%d", len(before.Mails))
	}
	first.From = start.AddDate(0, 0, 20)
	first.To = first.From.Add(10 * time.Minute)
	first.Now = start.AddDate(0, 0, 399)
	if _, err := SettleLaborDay(ctx, r, c, first); !errors.Is(err, ErrObservationStale) {
		t.Fatalf("已清理旧日期可重新发奖%v", err)
	}
	after, _, _ := r.Mailbox.Load(ctx, "1")
	if len(after.Mails) != len(before.Mails) {
		t.Fatal("旧观察发奖")
	}
	// 修改开放时间不重置账号已经领取的三份登录奖励。
	c.Policy.Starts = start.AddDate(0, 0, 399)
	from := start.AddDate(0, 0, 400)
	next := LaborDayCommand{AccountID: "a", CharacterID: "1", Now: from.Add(10 * time.Minute), From: from, To: from.Add(10 * time.Minute), Login: true, Deliver: deliver}
	snapshot, err := SettleLaborDay(ctx, r, c, next)
	if err != nil || snapshot.LoginDays != 3 || snapshot.TodayPaid != 1 {
		t.Fatalf("修改开放时间破坏旧记录：%+v %v", snapshot, err)
	}
	after, _, _ = r.Mailbox.Load(ctx, "1")
	if len(after.Mails) != 404 {
		t.Fatalf("改时间重复登录发奖%d", len(after.Mails))
	}

}

type portedFailAfterApply struct {
	repo.MailboxAssetUnitOfWork
	unit repo.AccountTradeMailboxUnitOfWork
}

func (f portedFailAfterApply) WithinAccountTradeMailbox(ctx context.Context, a string, ids []string, mail string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return f.unit.WithinAccountTradeMailbox(ctx, a, ids, mail, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, mr repo.MailboxRepository) error {
		if err := fn(ar, cr, ir, mr); err != nil {
			return err
		}
		return errors.New("模拟提交前失败")
	})
}
func TestPortedReturneeConcurrentClaimAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, ChinaCalendar)
	state := ReturneeState{Started: now, ExpiresAt: now.Add(30 * 24 * time.Hour), LastSeen: now}
	state.Progress[0] = 1
	raw, _ := json.Marshal(state)
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{ReturneeMetadataKey: string(raw), "keep": "untouched"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "1", Level: 1, Stats: map[string]int64{"keep": 23}}); err != nil {
		t.Fatal(err)
	}
	c := &ReturneeCatalog{}
	for i := range c.Missions {
		c.Missions[i] = ReturneeMission{ID: uint32(i + 1), Condition: "clear quest", Required: 1, Reward: AttendancePVFItemReward{ItemID: 100, Count: 1}}
	}
	cmd := ReturneeCommand{AccountID: "a", CharacterID: "1", Now: now, Claim: 1, Deliver: func(box repo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(ctx, box, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "test", CompletionKey: key, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}}
	broken := r
	broken.MailboxAssets = portedFailAfterApply{r.MailboxAssets, r.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork)}
	if _, ids, err := SettleReturnee(ctx, broken, c, cmd); err == nil || len(ids) != 0 {
		t.Fatal("失败返回已回滚邮件")
	}
	a, _, _ := r.Account.Load(ctx, "a")
	box, _, _ := r.Mailbox.Load(ctx, "1")
	ch, _, _ := r.Character.Load(ctx, "1")
	if a.Metadata[ReturneeMetadataKey] != string(raw) || len(box.Mails) != 0 || ch.Stats["is_return_user"] != 0 {
		t.Fatal("事务失败泄漏状态")
	}
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := SettleReturnee(ctx, r, c, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, _, _ = r.Account.Load(ctx, "a")
	saved, err := LoadReturneeState(a)
	if err != nil || saved.Claimed != 1 {
		t.Fatalf("回执%+v %v", saved, err)
	}
	box, _, _ = r.Mailbox.Load(ctx, "1")
	ch, _, _ = r.Character.Load(ctx, "1")
	if len(box.Mails) != 1 || a.Metadata["keep"] != "untouched" || ch.Stats["keep"] != 23 {
		t.Fatal("并发重复发奖或覆盖其它状态")
	}
	cmd.AccountID = "foreign"
	if _, _, err := SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("跨账号领取")
	}
	cmd.AccountID = "a"
	cmd.Validate = func() bool { return false }
	if _, _, err := SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("旧会话可以领取")
	}
}
