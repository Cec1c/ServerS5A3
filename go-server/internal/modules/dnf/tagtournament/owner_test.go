package tagtournament

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func ownerFixture(t *testing.T) (*Owner, repo.Group, *time.Time) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, beijing)
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"无关账号项": "保留"}}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"1", "2"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Slot: i, Name: "角色" + id, Level: 90, Stats: map[string]int64{"gold": 1000000, "fatigue": 123, "无关属性": 42}}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := LoadCatalog(catalogSource(t))
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(c, g)
	if err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return now }
	o.roll = func() (byte, error) { return 0, nil }
	if err = o.RegisterRoster(ctx, "a", "1", [2]string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	return o, g, &now
}

func admitRun(t *testing.T, o *Owner, mode Mode) *Run {
	t.Helper()
	a, err := o.Admit(context.Background(), "a", "1", [2]string{"1", "2"}, mode)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRun(a, [2]Vitals{{1000, 1000, 500, 500}, {2000, 2000, 1000, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func clearRound(t *testing.T, r *Run, now *time.Time) DailyEncounter {
	t.Helper()
	*now = now.Add(time.Second)
	e, _, err := r.BeginNext(*now, r.Snapshot().Phase == SpecialChoice)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err = r.ConfirmBossDefeated(*now, e.MapID, e.MonsterID); err != nil {
		t.Fatal(err)
	}
	return e
}

func mailTotals(t *testing.T, g repo.Group) map[int64]int64 {
	t.Helper()
	total := map[int64]int64{}
	for _, id := range []string{"1", "2"} {
		m, _, err := g.Mailbox.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, mail := range m.Mails {
			for _, a := range mail.Attachments {
				total[a.ItemID] += a.Count
			}
		}
	}
	return total
}

func TestOwnerAccountFeeModeLockResetAndConcurrentEntry(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Normal); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := g.Character.Load(ctx, "1")
	if c.Stats["gold"] != 880000 || c.Stats["fatigue"] != 123 || c.Stats["无关属性"] != 42 {
		t.Fatalf("并发多扣费或破坏其他属性: %+v", c.Stats)
	}
	if _, err := o.Admit(ctx, "a", "2", [2]string{"1", "2"}, Hard); err == nil {
		t.Fatal("支付后可切困难模式")
	}
	a, err := o.Admit(ctx, "a", "2", [2]string{"1", "2"}, Normal)
	if err != nil || a.Fee() != 0 {
		t.Fatal("同账号另一角色重复扣费", err)
	}
	*now = time.Date(2026, 10, 20, 5, 59, 59, 0, beijing)
	if a, err = o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Normal); err != nil || a.Fee() != 0 {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if a, err = o.Admit(ctx, "a", "2", [2]string{"1", "2"}, Hard); err != nil || a.Fee() != 120000 {
		t.Fatal("06点未重置或未可换模式", err)
	}
	c, _, _ = g.Character.Load(ctx, "2")
	if c.Stats["gold"] != 880000 {
		t.Fatal("新日没有扣当前角色金币")
	}
	account, _, _ := g.Account.Load(ctx, "a")
	if account.Metadata["无关账号项"] != "保留" {
		t.Fatal("账号其他元数据丢失")
	}
}

func TestOwnerThursdayHardEntryRewardsAndSixOClockResetWithoutRanking(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	admit := func(wantFee uint32) *Run {
		t.Helper()
		a, err := o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Hard)
		if err != nil || a.Fee() != wantFee {
			t.Fatalf("周四%v入场或日费错误: %+v %v", *now, a, err)
		}
		r, err := NewRun(a, [2]Vitals{{1000, 1000, 500, 500}, {2000, 2000, 1000, 1000}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	*now = time.Date(2026, 10, 22, 5, 29, 59, 0, beijing)
	admit(120000)
	*now = now.Add(time.Second)
	r := admit(0)
	for i := 0; i < 7; i++ {
		clearRound(t, r, now)
	}
	if _, err := o.Settle(ctx, r); err != nil {
		t.Fatal("周四原排行窗口中通关不能发奖", err)
	}
	if _, err := o.Settle(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := mailTotals(t, g); got[100] != 27 || got[101] != 30 {
		t.Fatal("周四困难奖励缺失或重复", got)
	}
	*now = time.Date(2026, 10, 22, 5, 59, 59, 0, beijing)
	admit(0)
	*now = now.Add(time.Second)
	rNext := admit(120000)
	if _, err := o.Settle(ctx, r); err == nil {
		t.Fatal("取消排行后跨日旧挑战仍可结算")
	}
	for i := 0; i < 7; i++ {
		clearRound(t, rNext, now)
	}
	if _, err := o.Settle(ctx, rNext); err != nil {
		t.Fatal(err)
	}
	if got := mailTotals(t, g); got[100] != 54 || got[101] != 60 {
		t.Fatal("取消排行影响06点后新日通关奖励", got)
	}
	ch, _, err := g.Character.Load(ctx, "1")
	if err != nil || ch.Stats["gold"] != 760000 || ch.Stats["fatigue"] != 123 {
		t.Fatal("周四跨日费用或其他属性被错误修改", ch.Stats, err)
	}
}

func TestOwnerNormalPerMonsterAcrossReentryAndCharacters(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	r := admitRun(t, o, Normal)
	for i := 0; i < 4; i++ {
		clearRound(t, r, now)
		v, err := o.Settle(ctx, r)
		if err != nil || len(v.MailIDs) != 1 {
			t.Fatal(v, err)
		}
		if v, err = o.Settle(ctx, r); err != nil || len(v.MailIDs) != 0 {
			t.Fatal("重复结算多发", v, err)
		}
	}
	if r.Snapshot().Phase != SpecialChoice {
		t.Fatal("普通四关后没有等待选王")
	}
	*now = now.Add(time.Second)
	if _, _, err := r.BeginNext(*now, false); err != nil || r.Snapshot().Phase != Finished {
		t.Fatal("拒绝王房后状态错误", err)
	}
	if got := mailTotals(t, g); got[100] != 8 || got[101] != 12 {
		t.Fatalf("普通奖励: %+v", got)
	}
	a, err := o.Admit(ctx, "a", "2", [2]string{"1", "2"}, Normal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Settle(ctx, r); err == nil {
		t.Fatal("新挑战后旧凭据还能结算")
	}
	r, err = NewRun(a, [2]Vitals{{1000, 1000, 500, 500}, {2000, 2000, 1000, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		clearRound(t, r, now)
		if _, err = o.Settle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := mailTotals(t, g); got[100] != 13 || got[101] != 15 {
		t.Fatalf("重入重复领怪物或王奖励错误: %+v", got)
	}
	m, _, _ := g.Mailbox.Load(ctx, "2")
	if len(m.Mails) != 1 {
		t.Fatal("另一角色应只领此前未打的王奖励")
	}
}

func TestOwnerHardCumulativeDifferenceAndStageDelivery(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	maxCleared := 0
	for _, rounds := range []int{3, 2, 7, 7} {
		r := admitRun(t, o, Hard)
		for i := 0; i < rounds; i++ {
			clearRound(t, r, now)
			result, err := o.Settle(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if i+1 > maxCleared {
				if len(result.MailIDs) != 1 || len(result.Rewards) != 2 {
					t.Fatal("新阶段没有即时发放两种材料", result)
				}
				maxCleared = i + 1
			} else if len(result.MailIDs) != 0 {
				t.Fatal("已领取阶段重复发奖", result)
			}
			if again, err := o.Settle(ctx, r); err != nil || len(again.MailIDs) != 0 {
				t.Fatal("重复结算不幂等", again, err)
			}
		}
		if rounds < 7 {
			*now = now.Add(time.Second)
			if err := r.Abandon(*now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := o.Settle(ctx, r); err != nil {
			t.Fatal(err)
		}
		if rounds == 3 {
			if got := mailTotals(t, g); got[100] != 9 || got[101] != 10 {
				t.Fatalf("第三关把列表累加了: %+v", got)
			}
		}
	}
	if got := mailTotals(t, g); got[100] != 27 || got[101] != 30 {
		t.Fatalf("困难反复进入/更高进度差额错误: %+v", got)
	}
	m, _, _ := g.Mailbox.Load(ctx, "1")
	if len(m.Mails) != 7 {
		t.Fatalf("应只投递七个新阶段的差额: %d", len(m.Mails))
	}
	for _, mail := range m.Mails {
		if len(mail.Attachments) != 2 || !strings.HasPrefix(mail.Title, "帝国竞技场第") {
			t.Fatal("阶段邮件缺少两种材料或回合标题", mail)
		}
	}
}

type faultTournamentTx struct {
	base   repo.AccountTradeMailboxUnitOfWork
	before func()
	after  error
}

func (f faultTournamentTx) WithinAccountTradeMailbox(ctx context.Context, a string, ids []string, mail string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return f.base.WithinAccountTradeMailbox(ctx, a, ids, mail, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, mr repo.MailboxRepository) error {
		if f.before != nil {
			f.before()
		}
		if err := apply(ar, cr, ir, mr); err != nil {
			return err
		}
		return f.after
	})
}

func TestOwnerRollbackEntryAndRewardRetry(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	base := o.tx
	o.tx = faultTournamentTx{base: base, after: errors.New("模拟提交失败")}
	if a, err := o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Normal); err == nil || a.token != "" {
		t.Fatal("失败返回可用凭据")
	}
	c, _, _ := g.Character.Load(ctx, "1")
	if c.Stats["gold"] != 1000000 {
		t.Fatal("失败入场扣了金币")
	}
	o.tx = base
	r := admitRun(t, o, Normal)
	clearRound(t, r, now)
	o.tx = faultTournamentTx{base: base, after: errors.New("模拟邮件写后失败")}
	if s, err := o.Settle(ctx, r); err == nil || len(s.MailIDs) != 0 {
		t.Fatal("失败返回奖励成功")
	}
	if len(mailTotals(t, g)) != 0 {
		t.Fatal("失败奖励留了邮件")
	}
	o.tx = base
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := o.Settle(ctx, r); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := mailTotals(t, g); got[100] != 2 || got[101] != 3 {
		t.Fatal("重试/并发重复发奖", got)
	}
}

func TestOwnerClockSampledInsideTransactionAndExpiredRunCannotSettle(t *testing.T) {
	o, g, now := ownerFixture(t)
	ctx := context.Background()
	r := admitRun(t, o, Normal)
	clearRound(t, r, now)
	*now = time.Date(2026, 10, 20, 5, 59, 59, 0, beijing)
	o.tx = faultTournamentTx{base: o.tx, before: func() { *now = time.Date(2026, 10, 20, 6, 0, 0, 0, beijing) }}
	a, err := o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Normal)
	if err != nil || a.Fee() != 120000 {
		t.Fatal("事务等待跨日仍按旧时间免费", err)
	}
	if _, err = o.Settle(ctx, r); err == nil {
		t.Fatal("过期挑战能领新一天奖励")
	}
	if len(mailTotals(t, g)) != 0 {
		t.Fatal("过期挑战生成了邮件")
	}
}

func TestOwnerRejectsCorruptionForeignDeletedAndUnregisteredRoster(t *testing.T) {
	for _, scenario := range []string{"重名队员", "外国账号", "低等级", "已删除", "未注册", "损坏日账本", "尾随日账本"} {
		t.Run(scenario, func(t *testing.T) {
			o, g, _ := ownerFixture(t)
			ctx := context.Background()
			ids := [2]string{"1", "2"}
			mode := Normal
			switch scenario {
			case "重名队员":
				ids[1] = "1"
			case "外国账号", "低等级", "已删除":
				ch, _, _ := g.Character.Load(ctx, "2")
				if scenario == "外国账号" {
					ch.AccountID = "b"
				}
				if scenario == "低等级" {
					ch.Level = 89
				}
				if scenario == "已删除" {
					ch.Stats["delete_flag"] = 1
				}
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			case "未注册":
				ids = [2]string{"2", "1"}
			case "损坏日账本", "尾随日账本":
				ac, _, _ := g.Account.Load(ctx, "a")
				ac.Metadata[dayKey] = "{损坏"
				if scenario == "尾随日账本" {
					ac.Metadata[dayKey] = "{} {}"
				}
				if err := g.Account.Save(ctx, ac); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := o.Admit(ctx, "a", "1", ids, mode); err == nil {
				t.Fatal("非法入场被允许")
			}
			ch, _, _ := g.Character.Load(ctx, "1")
			if ch.Stats["gold"] != 1000000 {
				t.Fatal("拒绝后金币变了")
			}
		})
	}
	if _, err := NewOwner(candidateCatalog(t), repo.Group{}); err == nil {
		t.Fatal("缺少事务时不能降级独立Save")
	}
}

func TestAdmissionSnapshotDoesNotShareCharacterOrPlanState(t *testing.T) {
	o, _, _ := ownerFixture(t)
	a, err := o.Admit(context.Background(), "a", "1", [2]string{"1", "2"}, Normal)
	if err != nil {
		t.Fatal(err)
	}
	chars := a.Characters()
	chars[0].Stats["gold"] = 0
	p := a.Plan()
	p.Encounters[0].MonsterID = 0
	if a.Characters()[0].Stats["gold"] == 0 || a.Plan().Encounters[0].MonsterID == 0 {
		t.Fatal("可变快照共享")
	}
	if _, err = NewRun(Admission{}, [2]Vitals{}); err == nil {
		t.Fatal("可伪造未支付挑战")
	}
	if _, err = NewRun(a, [2]Vitals{{0, 100, 0, 100}, {100, 100, 0, 100}}); err == nil {
		t.Fatal("以死亡角色初始化")
	}
	if _, err = decodeTestUnknown(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal(fmt.Sprint(err))
	}
}

func decodeTestUnknown() (DayState, error) {
	var v DayState
	err := decodeMetadata(`{"未知字段":1}`, &v)
	return v, err
}

func TestRealPVFTagTournamentAccountEntryAndMailSettlement(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF，账号结算只读专项未执行")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{Normal, Hard} {
		t.Run(fmt.Sprintf("模式%d", mode), func(t *testing.T) {
			o, g, now := ownerFixture(t)
			o.catalog = catalog
			ctx := context.Background()
			r := admitRun(t, o, mode)
			if r.admission.Fee() != 150000 {
				t.Fatal("没有读取真实15万费用")
			}
			rounds := 5
			if mode == Hard {
				rounds = 7
			}
			for i := 0; i < rounds; i++ {
				clearRound(t, r, now)
				if mode == Normal {
					if _, err := o.Settle(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == Hard {
				if _, err := o.Settle(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			wantA, wantB := int64(13), int64(15)
			if mode == Hard {
				wantA, wantB = 27, 30
			}
			if got := mailTotals(t, g); got[10304532] != wantA || got[10304533] != wantB {
				t.Fatal("真实物品编号或数量错误", got)
			}
			if _, err := o.RecordCompletion(ctx, r); err != nil {
				t.Fatal(err)
			}
			state, err := o.Weekly(ctx, "a")
			if err != nil || state.CanClaim != (mode == Hard) {
				t.Fatal("真实通关周奖励资格错误", state, err)
			}
			if mode == Hard {
				if _, err = o.ClaimWeekly(ctx, "a", "2"); err != nil {
					t.Fatal(err)
				}
				if _, err = o.ClaimWeekly(ctx, "a", "1"); err != nil {
					t.Fatal(err)
				}
				if mailTotals(t, g)[int64(catalog.WeeklyReward().ID)] != 1 {
					t.Fatal("真实字体漏发或重复发放")
				}
			}
			ch, _, _ := g.Character.Load(ctx, "1")
			if ch.Stats["gold"] != 850000 || ch.Stats["fatigue"] != 123 {
				t.Fatal("真实入场资产错误", ch.Stats)
			}
			if s, err := o.Settle(ctx, r); err != nil || len(s.MailIDs) != 0 {
				t.Fatal("真实配置重发奖励", s, err)
			}
		})
	}
}
