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

func TestReturneeNewbieFirstEntryOnePeriodAndGroupIsolation(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 31, 5, 59, 0, 0, ChinaCalendar)
	c := &ReturneeCatalog{Newbie: &ReturneeCatalog{}}
	conditions := []string{"move village", "join guild", "learn skill", "level up", "grow job", "disjoint item", "use cube skill", "mission clear count", "mission clear count", "mission clear count"}
	newIDs := []uint32{40, 41, 42, 43, 44, 45, 46, 17, 18, 19}
	oldIDs := []uint32{47, 48, 49, 50, 51, 52, 53, 20, 21, 22}
	for i := range c.Missions {
		c.Missions[i] = ReturneeMission{ID: oldIDs[i], Condition: "clear quest", Required: 1, Reward: AttendancePVFItemReward{ItemID: 100, Count: 1}}
		c.Newbie.Missions[i] = ReturneeMission{ID: newIDs[i], Condition: conditions[i], Required: 1, Reward: AttendancePVFItemReward{ItemID: 100, Count: 1}}
	}
	c.Newbie.Missions[0].ConditionValue = 38
	c.Newbie.Missions[2].Required = 5
	c.Newbie.Missions[3].ConditionValue = 10
	for i := range c.Newbie.Attendance {
		c.Newbie.Attendance[i] = ReturneeMission{ID: uint32(26 + i), Condition: "attendance count", Required: 1, Reward: AttendancePVFItemReward{ItemID: 100, Count: 1}}
	}
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{NewbieEligibleMetadataKey: "pending"}}); err != nil {
		t.Fatal(err)
	}
	ch := repo.CharacterRecord{AccountID: "a", CharacterID: "1", Level: 1, Stats: map[string]int64{}}
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cmd := ReturneeCommand{AccountID: "a", CharacterID: "1", Now: now, Login: true, Deliver: func(m repo.MailboxRepository, reward AttendancePVFItemReward, key string) (string, error) {
		return repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "新手测试", CompletionKey: key, CreatedAt: now, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}}
	// 资格开始与标记消费也须随原事务失败回滚。
	broken := r
	broken.MailboxAssets = portedFailAfterApply{r.MailboxAssets, r.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork)}
	if _, _, err := SettleReturnee(ctx, broken, c, cmd); err == nil {
		t.Fatal("资格事务故障未返回")
	}
	a, _, _ := r.Account.Load(ctx, "a")
	if a.Metadata[NewbieEligibleMetadataKey] != "pending" || a.Metadata[ReturneeMetadataKey] != "" {
		t.Fatal("失败仍开始新手周期")
	}
	s, _, err := SettleReturnee(ctx, r, c, cmd)
	if err != nil || !s.Newbie || s.UserType != 0 || !s.Started.Equal(now) || !s.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || len(s.AttendanceDays) != 1 {
		t.Fatal(s, err)
	}
	live, _, _ := r.Character.Load(ctx, "1")
	if live.Stats["is_return_user"] != 0 || live.Stats["seria_returnee_active_until"] != 0 || !repo.SeriaWelcomeActiveAt(live, now) {
		t.Fatal("新手被记成回归或未启用计数", live.Stats)
	}
	cmd.Claim = 47
	if _, _, err := SettleReturnee(ctx, r, c, cmd); err == nil {
		t.Fatal("新手领取了回归任务")
	}
	cmd.Claim = 26
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
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
	box, _, _ := r.Mailbox.Load(ctx, "1")
	if len(box.Mails) != 1 {
		t.Fatal("并发重复发奖", len(box.Mails))
	}
	ch = live
	ch.Level = 10
	ch.Stats["grow_type"] = 1
	ch.Stats[ReturneeSkillCountKey] = 5
	repo.RecordReturneeVillage(&ch, 38, now.Add(time.Minute))
	repo.AddReturneeCounter(&ch, repo.ReturneeCubeSkillCountKey, now.Add(time.Minute))
	ch.Stats[ReturneeDisjointCountKey] = 1
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cmd.Claim = 0
	cmd.Now = now.Add(2 * time.Minute)
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.Progress[0] != 1 || s.Progress[2] != 5 || s.Progress[3] != 1 || s.Progress[4] != 1 || s.Progress[5] != 1 || s.Progress[6] != 1 || len(s.AttendanceDays) != 2 || !s.Started.Equal(now) {
		t.Fatal("权威任务或跨日签到/开始时间有误", s, err)
	}
	// 换角色不能刷新周期，也不能把另一角色历史学习计入任务。
	if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: "2", Level: 1, Stats: map[string]int64{ReturneeSkillCountKey: 999}}); err != nil {
		t.Fatal(err)
	}
	cmd.CharacterID = "2"
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || !s.Started.Equal(now) || s.AttendanceClaimed != 1 || s.Progress[2] != 5 {
		t.Fatal("换角继承有误", s, err)
	}
	// 连续在线到期不续期，标记已消费；之后离线满七天可进入原回归组。
	cmd.Now = now.Add(NewbieDuration)
	cmd.Login = false
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.ActiveAt(cmd.Now) {
		t.Fatal(s, err)
	}
	cmd.Login = true
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.ActiveAt(cmd.Now) {
		t.Fatal("新手到期重新领取周期", s, err)
	}
	cmd.Now = cmd.Now.Add(7 * 24 * time.Hour)
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.Newbie || !s.ActiveAt(cmd.Now) || s.UserType == 0 {
		t.Fatal("过期新手不能正常回归", s, err)
	}
	// 旧默认UserType=0仍按历史回归记录解释，不能迁移成新手。
	a, _, _ = r.Account.Load(ctx, "a")
	s.Newbie = false
	s.UserType = 0
	raw, _ := json.Marshal(s)
	a.Metadata[ReturneeMetadataKey] = string(raw)
	if err := r.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	s, _, err = SettleReturnee(ctx, r, c, cmd)
	if err != nil || s.Newbie {
		t.Fatal("旧回归记录被改成新手", s, err)
	}
}
