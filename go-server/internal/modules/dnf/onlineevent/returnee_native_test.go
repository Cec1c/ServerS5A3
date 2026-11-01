package onlineevent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestReturneeNativeCountersConsumeOnlyNewSuccessfulActions(t *testing.T) {
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, ChinaCalendar)
	c := &ReturneeCatalog{}
	conditions := []string{"move village", "use cube skill", "dimension dungeon clear", "mission clear count"}
	for i, condition := range conditions {
		c.Missions[i] = ReturneeMission{ID: uint32(i + 1), Condition: condition, Required: 1, ConditionValue: -1}
	}
	s := ReturneeState{Started: now, ExpiresAt: now.Add(ReturneeDuration)}
	o := ReturneeObservation{CubeSkills: 100, Dimensions: 200, Villages: map[int64]int64{38: now.Add(-time.Second).UnixNano()}}
	s.Observe(c, "1", o, now, false)
	if s.Progress[0] != 0 || s.Progress[1] != 0 || s.Progress[2] != 0 {
		t.Fatal("历史行为被计入", s.Progress)
	}
	o.CubeSkills++
	o.Dimensions++
	o.Villages[38] = now.Add(time.Second).UnixNano()
	s.Observe(c, "1", o, now.Add(time.Second), false)
	s.Observe(c, "1", o, now.Add(time.Second), false)
	for i := 0; i < 4; i++ {
		if s.Progress[i] != 1 {
			t.Fatal("新行为、汇总或幂等有误", s.Progress)
		}
	}
	other := ReturneeObservation{CubeSkills: 999, Dimensions: 999}
	s.Progress[1] = 0
	s.Progress[2] = 0
	s.Observe(c, "2", other, now.Add(time.Second), false)
	if s.Progress[1] != 0 || s.Progress[2] != 0 {
		t.Fatal("换角把另一角色历史当新进度")
	}
}

func TestReturneeNativeSevenAttendanceDaysConcurrentClaimsAndRollback(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 30, 5, 59, 0, 0, ChinaCalendar)
	c := &ReturneeCatalog{}
	for i := range c.Missions {
		c.Missions[i] = ReturneeMission{ID: uint32(i + 1), Condition: "clear quest", Required: 1, Reward: AttendancePVFItemReward{ItemID: 100, Count: 1}}
	}
	for i := range c.Attendance {
		c.Attendance[i] = ReturneeMission{ID: uint32(33 + i), Condition: "attendance count", Required: 1, Reward: AttendancePVFItemReward{ItemID: int64(200 + i), Count: 1}}
	}
	s := ReturneeState{LastSeen: now.Add(-ReturneeOfflinePeriod)}
	raw, _ := json.Marshal(s)
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{ReturneeMetadataKey: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Level: 90, Stats: map[string]int64{}}); err != nil {
			t.Fatal(err)
		}
	}
	cmd := ReturneeCommand{AccountID: "a", CharacterID: "1", Now: now, Login: true, Deliver: func(box repo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(ctx, box, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "回归测试", CompletionKey: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}}
	s, _, err := SettleReturnee(ctx, r, c, cmd)
	if err != nil || len(s.AttendanceDays) != 1 {
		t.Fatal(s, err)
	}
	cmd.Login = false
	cmd.Claim = 34
	if _, _, err := SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("第一天可领第二天奖励")
	}
	cmd.Claim = 33
	broken := r
	broken.MailboxAssets = portedFailAfterApply{r.MailboxAssets, r.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork)}
	if _, _, err := SettleReturnee(ctx, broken, c, cmd); err == nil {
		t.Fatal("回滚故障未返回")
	}
	a, _, _ := r.Account.Load(ctx, "a")
	s, _ = LoadReturneeState(a)
	box, _, _ := r.Mailbox.Load(ctx, "1")
	if s.AttendanceClaimed != 0 || len(box.Mails) != 0 {
		t.Fatal("失败签到泄漏邮件或领取位")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
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
	box, _, _ = r.Mailbox.Load(ctx, "1")
	if len(box.Mails) != 1 {
		t.Fatal("并发重复签到邮件", len(box.Mails))
	}
	cmd.CharacterID = "2"
	cmd.Claim = 0
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || len(s.AttendanceDays) != 1 {
		t.Fatal("换角重复签到", s, err)
	}
	cmd.CharacterID = "1"
	cmd.Now = now.Add(time.Minute)
	cmd.Claim = 34
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || len(s.AttendanceDays) != 2 {
		t.Fatal("北京时间06点未换日", s, err)
	}
	for day := 2; day < 7; day++ {
		cmd.Now = now.Add(time.Minute).AddDate(0, 0, day-1)
		cmd.Claim = uint32(33 + day)
		if _, _, err = SettleReturnee(ctx, r, c, cmd); err != nil {
			t.Fatal(day, err)
		}
	}
	box, _, _ = r.Mailbox.Load(ctx, "1")
	if len(box.Mails) != 7 {
		t.Fatal("七天奖励错误", len(box.Mails))
	}
	cmd.Claim = 40
	if _, _, err = SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("未知任务被领取")
	}
	cmd.Claim = 0
	extra := uint32(511)
	guide := uint32(45)
	cmd.Guide = &guide
	cmd.GuideExtra = &extra
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.Guide != uint64(1)<<45 || s.GuideExtra[63] != 128 {
		t.Fatal("两类引导位未持久化", s, err)
	}
	extra = 512
	if _, _, err = SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("越界扩展引导被接受")
	}
	cmd.GuideExtra = nil
	cmd.Guide = nil
	cmd.Now = s.ExpiresAt
	cmd.Claim = 33
	if _, _, err = SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("到期仍可领取")
	}
}
