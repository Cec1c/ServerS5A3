package onlineevent

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPrivateAutomaticNewCharactersHaveNoAccountQuota(t *testing.T) {
	now := time.Now().UTC()
	c := &LevelChallengeCatalog{Limit: 1, Job: "[all]", AutomaticNewCharacters: true}
	a := repo.AccountRecord{AccountID: "all-new", Metadata: map[string]string{"keep": "unchanged"}}
	for id := 1; id <= 300; id++ {
		ch := repo.CharacterRecord{CharacterID: strconv.Itoa(id), AccountID: a.AccountID, Job: "0", Level: 1, CreatedAt: now}
		if err := c.Register(&a, &ch); err != nil || ch.Stats[LevelChallengeRoleStat] != 1 {
			t.Fatalf("new character %d did not join: %v", id, err)
		}
	}
	state, err := ReadLevelChallengeState(a)
	if err != nil || len(state) != 300 || a.Metadata["keep"] != "unchanged" {
		t.Fatal(len(state), err)
	}
	for _, ch := range []repo.CharacterRecord{
		{CharacterID: "1", AccountID: a.AccountID, Job: "0", Level: 1, CreatedAt: now},
		{CharacterID: "301", AccountID: a.AccountID, Job: "0", Level: 2, CreatedAt: now},
		{CharacterID: "302", AccountID: a.AccountID, Job: "0", Level: 1, CreatedAt: now, Stats: map[string]int64{"pvp_private_character": 1}},
	} {
		if err := c.Register(&a, &ch); err == nil {
			t.Fatal("duplicate/existing/PVP role admitted", ch.CharacterID)
		}
	}
}

func TestLevelChallengeRewardRollbackConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	r := memory.NewMemoryGroup()
	c := &LevelChallengeCatalog{Limit: 1, Job: "[all]", Main: []LegacyLevelReward{{Level: 20, Item: AttendancePVFItemReward{ItemID: 100, Count: 5}}}, Bonus: []LegacyLevelReward{{Level: 10, Item: AttendancePVFItemReward{ItemID: 101, Count: 1}}, {Level: 20, Item: AttendancePVFItemReward{ItemID: 102, Count: 1}}}}
	a := repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"unrelated": "keep"}}
	ch := repo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: "8", Level: 1, CreatedAt: now}
	if err := c.Register(&a, &ch); err != nil {
		t.Fatal(err)
	}
	second := ch
	second.CharacterID = "2"
	if err := c.Register(&a, &second); err == nil {
		t.Fatal("重复预约名额未阻止")
	}
	ch.Level = 20
	if err := r.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	fail := true
	cmd := LevelChallengeCommand{AccountID: "a", CharacterID: "1", Now: now, Deliver: func(m repo.MailboxRepository, reward AttendancePVFItemReward, key string, _ bool) (string, error) {
		if fail && reward.ItemID == 102 {
			return "", errors.New("模拟中途发奖失败")
		}
		return repo.AppendSystemMail(ctx, m, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: "test", CompletionKey: key, Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count}}})
	}}
	if _, err := SettleLevelChallenge(ctx, r, c, cmd); err == nil {
		t.Fatal("发奖失败仍提交")
	}
	box, _, _ := r.Mailbox.Load(ctx, "1")
	if len(box.Mails) != 0 {
		t.Fatal("回滚残留邮件")
	}
	a, _, _ = r.Account.Load(ctx, "a")
	state, _ := ReadLevelChallengeState(a)
	if len(state["1"].Main)+len(state["1"].Bonus) != 0 {
		t.Fatal("回滚残留领奖记录")
	}
	fail = false
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := SettleLevelChallenge(ctx, r, c, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	box, _, _ = r.Mailbox.Load(ctx, "1")
	if len(box.Mails) != 3 {
		t.Fatalf("并发重试邮件数=%d", len(box.Mails))
	}
	a, _, _ = r.Account.Load(ctx, "a")
	if a.Metadata["unrelated"] != "keep" {
		t.Fatal("覆盖无关账号记录")
	}
	cmd.Validate = func() bool { return false }
	if _, err := SettleLevelChallenge(ctx, r, c, cmd); err == nil {
		t.Fatal("过期会话仍生效")
	}
	cmd.Validate = nil
	cmd.AccountID = "foreign"
	if _, err := SettleLevelChallenge(ctx, r, c, cmd); err == nil {
		t.Fatal("跨账号可发奖")
	}
}

func TestLevelChallengeEligibility(t *testing.T) {
	now := time.Now().UTC()
	c := &LevelChallengeCatalog{Limit: 1, Job: "[at mage]", Window: ActivityWindow{Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour)}}
	for _, tc := range []struct {
		job     string
		level   int
		private bool
		at      time.Time
		ok      bool
	}{
		{"8", 1, false, now, true}, {"1", 1, false, now, false}, {"8", 2, false, now, false}, {"8", 1, true, now, false}, {"8", 1, false, now.Add(2 * time.Hour), false},
	} {
		a := repo.AccountRecord{AccountID: "a"}
		ch := repo.CharacterRecord{CharacterID: "1", AccountID: "a", Job: tc.job, Level: tc.level, CreatedAt: tc.at, Stats: map[string]int64{}}
		if tc.private {
			ch.Stats["pvp_private_character"] = 1
		}
		if err := c.Register(&a, &ch); (err == nil) != tc.ok {
			t.Fatalf("资格=%+v err=%v", tc, err)
		}
	}
}

func TestRealPVFLevelChallengeCatalog(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需显式提供真实PVF")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadLevelChallengeCatalog(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limit != 1 || c.Job != "[all]" || len(c.Main) != 10 || len(c.Bonus) != 21 {
		t.Fatalf("现用PVF配置=%+v", c)
	}
	var count int64
	for _, row := range c.Main {
		count += row.Item.Count
		if row.Item.DefinitionPath == "" {
			t.Fatal("奖励未解析")
		}
	}
	if count != 300 || c.Main[9].Level != 90 || c.Bonus[20].Item.ItemID != 490009296 {
		t.Fatal("现用奖励表不匹配")
	}
}
