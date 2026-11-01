package dnfbridge

import (
	"context"
	"testing"
	"time"

	event "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// 暂停旧连接的首次补档Save，期间让另一连接完成真正初始化和首日领奖。
// 不依赖调度概率；按生产Load->Save边界确定性模拟两条请求的交错。
type activityAuditDelayedBootstrapAccount struct {
	repo.AccountRepository
	beforeSave func()
}

func (a *activityAuditDelayedBootstrapAccount) CreateIfAbsent(ctx context.Context, record repo.AccountRecord) (bool, error) {
	if f := a.beforeSave; f != nil {
		a.beforeSave = nil
		f()
	}
	return repo.CreateAccountIfAbsent(ctx, a.AccountRepository, record)
}

func TestActivityAuditBootstrapMustNotOverwriteStartedNewbiePeriod(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	stale := r
	account := &activityAuditDelayedBootstrapAccount{AccountRepository: r.Account}
	stale.Account = account
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return stale, true }}
	other := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	q := &gameSession{accountID: "audit-new", accountFromToken: true}
	q2 := &gameSession{accountID: "audit-new", accountFromToken: true}
	enablePortedActivityTest(t, s, q)
	enablePortedActivityTest(t, other, q2)
	catalog := &event.ReturneeCatalog{Newbie: &event.ReturneeCatalog{}}
	for i := range catalog.Missions {
		catalog.Missions[i] = event.ReturneeMission{ID: uint32(47 + i), Required: 1, Condition: "clear quest"}
		catalog.Newbie.Missions[i] = event.ReturneeMission{ID: uint32(40 + i), Required: 1, Condition: "clear quest"}
	}
	catalog.Newbie.Attendance[0] = event.ReturneeMission{ID: 26, Required: 1, Condition: "attendance count", Reward: event.AttendancePVFItemReward{ItemID: 100, Count: 1}}
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, event.ChinaCalendar)
	cmd := event.ReturneeCommand{AccountID: "audit-new", CharacterID: "1", Now: now, Login: true,
		Deliver: func(mail repo.MailboxRepository, reward event.AttendancePVFItemReward, key string) (string, error) {
			return repo.AppendSystemMail(ctx, mail, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "审计夹具", CompletionKey: key, CreatedAt: now,
				Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
		}}
	var startedRaw, marker string
	account.beforeSave = func() {
		if !other.ensureAccountLevelRecords(ctx, r, q2, "audit-new", accountRecordBootstrapLoginSource) {
			t.Fatal("另一个连接补档失败")
		}
		if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "audit-new", CharacterID: "1", Level: 1, Stats: map[string]int64{}}); err != nil {
			t.Fatal(err)
		}
		cmd.Claim = 26
		state, mails, err := event.SettleReturnee(ctx, r, catalog, cmd)
		if err != nil || !state.Newbie || state.AttendanceClaimed != 1 || len(mails) != 1 {
			t.Fatalf("合法新手初始化或领奖失败：%+v %v %v", state, mails, err)
		}
		a, _, _ := r.Account.Load(ctx, "audit-new")
		startedRaw, marker = a.Metadata[event.ReturneeMetadataKey], a.Metadata[event.NewbieEligibleMetadataKey]
		// 旧登录可在初始化期间被新登录撤销，补档Save本身却没有相应守卫。
		q.loginRevoked.Store(true)
	}
	if !s.ensureAccountLevelRecords(ctx, stale, q, "audit-new", accountRecordBootstrapLoginSource) {
		t.Fatal("旧补档请求失败")
	}
	if startedRaw == "" || marker == "" {
		t.Fatal("并发交错未实际执行，不能把未触发当作通过")
	}
	a, _, _ := r.Account.Load(ctx, "audit-new")
	if a.Metadata[event.ReturneeMetadataKey] == startedRaw && a.Metadata[event.NewbieEligibleMetadataKey] == marker {
		return
	}
	t.Logf("旧请求恢复后：资格记录=%q，新手标记=%q；已开始的周期和领取位被覆盖", a.Metadata[event.ReturneeMetadataKey], a.Metadata[event.NewbieEligibleMetadataKey])
	now = now.Add(time.Minute)
	cmd.Now = now
	state, _, err := event.SettleReturnee(ctx, r, catalog, cmd)
	if err != nil {
		t.Fatal(err)
	}
	box, _, _ := r.Mailbox.Load(ctx, "1")
	t.Logf("再次正常选角后：新周期=%v，首日签到邮件数=%d", state.Started, len(box.Mails))
	t.Fatalf("首次补档必须仅创建缺失账号，不能覆盖另一连接已提交的欢迎周期；当前首日签到邮件=%d（预期1）", len(box.Mails))
}
