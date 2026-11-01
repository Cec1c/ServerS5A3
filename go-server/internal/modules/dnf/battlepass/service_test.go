package battlepass

import (
	"context"
	"encoding/json"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (repo.Group, *Catalog, Policy, Command) {
	t.Helper()
	g := memory.NewMemoryGroup()
	for _, a := range []string{"a", "b"} {
		if e := g.Account.Save(nil, repo.AccountRecord{AccountID: a, Metadata: map[string]string{"keep": "yes"}}); e != nil {
			t.Fatal(e)
		}
	}
	for id, a := range map[string]string{"1": "a", "2": "a", "3": "b"} {
		if e := g.Character.Save(nil, repo.CharacterRecord{CharacterID: id, AccountID: a}); e != nil {
			t.Fatal(e)
		}
	}
	start := time.Date(2026, 10, 29, 6, 0, 0, 0, time.FixedZone("Asia/Shanghai", 28800))
	p := Policy{1, start, start.AddDate(0, 0, 100)}
	c := &Catalog{Missions: map[uint32]Mission{}, Daily: [][4]uint32{{1, 2, 3, 4}}, Weekly: [][4]uint32{{5, 6, 7, 8}}, PremiumRatio: 3}
	for i := uint32(1); i <= 26; i++ {
		xp := uint32(100)
		if i > 4 {
			xp = 1000
		}
		if i > 8 {
			xp = 1600
		}
		c.Missions[i] = Mission{i, 1, xp, 17, "分解装备", -1}
		if i > 8 {
			c.Season = append(c.Season, i)
		}
	}
	for i := 1; i <= 79; i++ {
		c.Experience[i] = 1300
	}
	c.Rewards[0][1] = []Reward{{15, 2}, {21, 1}}
	c.Rewards[1][1] = []Reward{{899, 1}}
	cmd := Command{AccountID: "a", CharacterID: "1", Now: start.Add(time.Hour), Fingerprint: "fixture-v1", Counters: []Counter{{Day: repo.ActivityDay(start), Kind: 17, Param: -1, Amount: 1}}}
	cmd.Deliver = func(m repo.MailboxRepository, rs []Reward, key string) (string, error) {
		var attachments []repo.MailAttachment
		for _, r := range rs {
			attachments = append(attachments, repo.MailAttachment{ItemID: int64(r.ItemID), Count: int64(r.Count)})
		}
		return repo.AppendSystemMail(context.Background(), m, repo.SystemMailDelivery{RecipientCharacterID: cmd.CharacterID, Source: key, CreatedAt: cmd.Now, Attachments: attachments})
	}
	return g, c, p, cmd
}
func query(t *testing.T, g repo.Group, c *Catalog, p Policy, cmd Command) Snapshot {
	t.Helper()
	v, e := Execute(nil, g, c, p, cmd)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func ready(cmd Command, p Policy) Command {
	cmd.Season = p.ID
	cmd.Day = DayNumber(cmd.Now)
	cmd.Week = WeekNumber(cmd.Now)
	return cmd
}
func TestBattlePassTaskClaimAtomicReplayAndAccountShared(t *testing.T) {
	g, c, p, cmd := fixture(t)
	first := query(t, g, c, p, cmd)
	if first.State.Level != 1 || first.Missions[0].Flags != 1 || first.Missions[3].Flags != 5 {
		t.Fatalf("初态%+v", first)
	}
	cmd = ready(cmd, p)
	cmd.Action, cmd.Category, cmd.Slot, cmd.Target = 1, 0, 0, 1
	v := query(t, g, c, p, cmd)
	if v.State.XP != 100 {
		t.Fatal("经验错误")
	}
	if _, e := Execute(nil, g, c, p, cmd); !errors.Is(e, ErrClaim) {
		t.Fatal("重复领取", e)
	}
	cmd.Action = 0
	cmd.CharacterID = "2"
	v = query(t, g, c, p, cmd)
	if v.State.XP != 100 || v.Missions[0].Flags&2 == 0 {
		t.Fatal("换角色丢失")
	}
	a, _, _ := g.Account.Load(nil, "a")
	if a.Metadata["keep"] != "yes" {
		t.Fatal("覆盖其他元数据")
	}
	cmd.CharacterID = "3"
	if _, e := Execute(nil, g, c, p, cmd); e == nil {
		t.Fatal("跨账号")
	}
}
func TestBattlePassConcurrentMailClaimAndRollback(t *testing.T) {
	g, c, p, cmd := fixture(t)
	query(t, g, c, p, cmd)
	cmd = ready(cmd, p)
	cmd.Action, cmd.Category, cmd.Target = 2, 0, 1
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = Execute(nil, g, c, p, cmd) }()
	}
	wg.Wait()
	m, _, e := g.Mailbox.Load(nil, "1")
	if e != nil || len(m.Mails) != 1 {
		t.Fatalf("并发重复发奖%d %v", len(m.Mails), e)
	}
	for _, mail := range m.Mails {
		if len(mail.Attachments) != 2 {
			t.Fatal("等级附加奖励漏发")
		}
	}
	g, c, p, cmd = fixture(t)
	cmd = ready(cmd, p)
	cmd.Action = 3
	cmd.Deliver = func(m repo.MailboxRepository, rs []Reward, key string) (string, error) {
		_, e := repo.AppendSystemMail(nil, m, repo.SystemMailDelivery{RecipientCharacterID: "1", Source: key, Attachments: []repo.MailAttachment{{ItemID: 15, Count: 1}}})
		if e != nil {
			return "", e
		}
		return "", errors.New("测试邮件写失败")
	}
	if _, e = Execute(nil, g, c, p, cmd); e == nil {
		t.Fatal("失败没有回滚")
	}
	m, _, _ = g.Mailbox.Load(nil, "1")
	if len(m.Mails) != 0 {
		t.Fatal("失败仍有奖励")
	}
	cmd.Action = 0
	v := query(t, g, c, p, cmd)
	if Claimed(v.State, 0, 1) {
		t.Fatal("失败仍标记已领")
	}
}
func TestBattlePassPremiumOnlyDailyFourthAndLevelCap(t *testing.T) {
	g, c, p, cmd := fixture(t)
	s := query(t, g, c, p, cmd).State
	s.Premium = true
	s.XP = 1250
	a, _, _ := g.Account.Load(nil, "a")
	raw, _ := json.Marshal(s)
	a.Metadata[MetadataKey] = string(raw)
	_ = g.Account.Save(nil, a)
	cmd = ready(cmd, p)
	cmd.Action, cmd.Slot, cmd.Target = 1, 3, 4
	v := query(t, g, c, p, cmd)
	if v.State.Level != 2 || v.State.XP != 250 || v.Missions[0].GrantedXP != 100 || v.Missions[4].GrantedXP != 1000 || v.Missions[8].GrantedXP != 1600 {
		t.Fatal("高级倍率或跨级错误", v.State)
	}
	s = v.State
	s.Level = 79
	s.XP = 1250
	s.DailyClaimed = 0
	raw, _ = json.Marshal(s)
	a.Metadata[MetadataKey] = string(raw)
	_ = g.Account.Save(nil, a)
	v = query(t, g, c, p, cmd)
	if v.State.Level != 80 || v.State.XP != 0 {
		t.Fatal("满级循环错误")
	}
}
func TestBattlePassDayWeekAndStaleCycle(t *testing.T) {
	g, c, p, cmd := fixture(t)
	cmd = ready(cmd, p)
	cmd.Action, cmd.Target = 1, 1
	query(t, g, c, p, cmd)
	stale := cmd
	stale.Now = cmd.Now.AddDate(0, 0, 1)
	if _, e := Execute(nil, g, c, p, stale); !errors.Is(e, ErrPeriod) {
		t.Fatal("旧周期未拒绝", e)
	}
	cmd.Now = stale.Now
	cmd.Action = 0
	v := query(t, g, c, p, cmd)
	if v.State.DailyClaimed != 0 || v.State.LoginDays != 2 || v.Missions[0].Progress != 0 {
		t.Fatal("跨日清理错误", v.State)
	}
	monday := time.Date(2026, 11, 2, 6, 0, 0, 0, p.Start.Location())
	if WeekNumber(monday.Add(-time.Second)) == WeekNumber(monday) {
		t.Fatal("周一六点未换周")
	}
}
func TestBattlePassRealPVFCatalog(t *testing.T) {
	path := os.Getenv("BATTLEPASS_REAL_PVF")
	if path == "" {
		t.Skip("需显式提供真实战令PVF")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := Load(a)
	if e != nil {
		t.Fatal(e)
	}
	total := 0
	for _, track := range c.Rewards {
		for _, rs := range track {
			total += len(rs)
		}
	}
	if len(c.Missions) != 70 || len(c.Daily) != 100 || len(c.Weekly) != 15 || len(c.Season) != 18 || total != 162 || c.Experience[79] != 1300 || c.Experience[80] != 0 {
		t.Fatal("真实配置不完整")
	}
	text, e := a.ReadText(Path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Parse(text + "\n[mission]\n1 1 100 17 `重复任务` -1\n[/mission]\n"); e == nil {
		t.Fatal("重复任务未拒绝")
	}
}
