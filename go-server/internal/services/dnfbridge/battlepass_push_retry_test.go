package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type battlePassRetryAccount struct {
	repo.AccountRepository
	failNext, unavailable atomic.Bool
	reads                 atomic.Int32
}

func (a *battlePassRetryAccount) Load(ctx context.Context, id string) (repo.AccountRecord, bool, error) {
	a.reads.Add(1)
	if a.failNext.Swap(false) || a.unavailable.Load() {
		return repo.AccountRecord{}, false, errors.New("测试：临时读取账号失败")
	}
	return a.AccountRepository.Load(ctx, id)
}

type battlePassRetryActivity struct {
	repo.ActivityRepository
	failNext atomic.Bool
	reads    atomic.Int32
}

func (a *battlePassRetryActivity) Summary(ctx context.Context, q repo.ActivityQuery) (repo.ActivitySummary, error) {
	a.reads.Add(1)
	if a.failNext.Swap(false) {
		return repo.ActivitySummary{}, errors.New("测试：临时读取疲劳失败")
	}
	return a.ActivityRepository.Summary(ctx, q)
}

type battlePassRetryFixture struct {
	s        *Service
	x        *gameSession
	g        repo.Group
	conn     *playerStatsWireConn
	account  *battlePassRetryAccount
	activity *battlePassRetryActivity
}

func newBattlePassRetryFixture(t *testing.T, clock gameplayTimeQueue) *battlePassRetryFixture {
	t.Helper()
	s, x, _, conn := playerStatsGameFixture(t)
	g, _ := s.repositoryProvider()
	f := &battlePassRetryFixture{s: s, x: x, g: g, conn: conn}
	f.account = &battlePassRetryAccount{AccountRepository: g.Account}
	f.activity = &battlePassRetryActivity{ActivityRepository: g.Activity}
	f.g.Account, f.g.Activity = f.account, f.activity
	f.g.BattlePassChanges = repo.NewBattlePassChangeFeed()
	s.repositoryProvider = func() (repo.Group, bool) { return f.g, true }
	s.gameplayTimers = clock
	c := &bp.Catalog{Missions: map[uint32]bp.Mission{}, Daily: [][4]uint32{{1, 2, 3, 4}}, Weekly: [][4]uint32{{5, 6, 7, 8}}}
	for i := uint32(1); i <= 26; i++ {
		c.Missions[i] = bp.Mission{ID: i, Kind: 0, Param: -1, Target: 1000, XP: 100}
		if i >= 9 {
			c.Season = append(c.Season, i)
		}
	}
	m := c.Missions[1]
	m.Kind, m.Target = 1, 1
	c.Missions[1] = m
	for i := 1; i < 80; i++ {
		c.Experience[i] = 1300
	}
	s.battlePassCatalog, s.battlePassFingerprint = c, "retry-fixture"
	if err := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	ch, _, _ := g.Character.Load(context.Background(), "2")
	ch.CharacterID, ch.AccountID = "2", "a"
	ch.Stats = map[string]int64{fmt.Sprintf("battlepass2:%s:1:-1", repo.ActivityDay(clock.Now())): 1}
	if err := g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	battlePassPreviewCall(t, s, x, 1, []byte{'B', 'P', 'E', '2', 51, 0, 0, 0, 9, 0, 0, 0})
	battlePassNotifyPacket(t, conn, 3)
	t.Cleanup(func() { playerStatsEdit(t, s, x, func() { x.battlePassPush.stop(s) }) })
	return f
}

func (f *battlePassRetryFixture) claim(t *testing.T) {
	t.Helper()
	p, err := currentBattlePassPolicy()
	if err != nil {
		t.Fatal(err)
	}
	now := f.s.gameplayNow()
	state := bp.State{Season: p.ID, Level: 1, Day: bp.DayNumber(now), Week: bp.WeekNumber(now), DailyClaimed: 1, StartUnix: p.Start.Unix(), EndUnix: p.End.Unix(), ConfigFingerprint: "retry-fixture"}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := f.g.Account.Load(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	a.Metadata[bp.MetadataKey] = string(data)
	if err = f.g.Account.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func (f *battlePassRetryFixture) flush(t *testing.T) {
	t.Helper()
	playerStatsEdit(t, f.s, f.x, func() {
		p := f.x.battlePassPush
		p.clock.Cancel(p.key)
		if err := f.s.flushBattlePassPush(f.x, p); err != nil {
			t.Fatal(err)
		}
	})
}

func battlePassRetryClock() *fakeCurrentDungeonDeathTimerQueue {
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = time.Date(2026, 10, 31, 10, 0, 0, 0, time.FixedZone("CN", 28800))
	return q
}

// 真实调度器和会话事件循环：一次临时错误之后，不靠新行为或打开窗口恢复。
func TestBattlePassPushRetryRecoversWithoutNewEvent(t *testing.T) {
	q := newProcessGameplayTimeQueue(nil)
	q.now = func() time.Time { return time.Date(2026, 10, 31, 10, 0, 0, 0, time.FixedZone("CN", 28800)) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	f := newBattlePassRetryFixture(t, q)
	f.claim(t)
	before, fatigueBefore := f.account.reads.Load(), f.activity.reads.Load()
	f.account.failNext.Store(true)
	f.g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true})
	battlePassNotifyPacket(t, f.conn, 1)
	if f.account.reads.Load() != before+2 || f.activity.reads.Load() != fatigueBefore {
		t.Fatal("临时账号错误未准确补试，或额外读取了疲劳")
	}
	select {
	case p := <-f.conn.packets:
		t.Fatalf("恢复后仍重复通知：%x", p)
	case <-time.After(750 * time.Millisecond):
	}
	if f.account.reads.Load() != before+2 {
		t.Fatal("成功后仍有空闲轮询")
	}
}

func TestBattlePassPushRetryPreservesBothDirtyFlags(t *testing.T) {
	f := newBattlePassRetryFixture(t, battlePassRetryClock())
	f.claim(t)
	a0, f0 := f.account.reads.Load(), f.activity.reads.Load()
	f.account.failNext.Store(true)
	f.g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true, Fatigue: true})
	f.flush(t)
	playerStatsEdit(t, f.s, f.x, func() {
		p := f.x.battlePassPush
		if !p.accountDirty || !p.fatigueDirty {
			t.Fatal("账号读取失败丢失了尚未执行的疲劳刷新")
		}
	})
	f.activity.failNext.Store(true)
	f.flush(t)
	playerStatsEdit(t, f.s, f.x, func() {
		p := f.x.battlePassPush
		if p.accountDirty || !p.fatigueDirty {
			t.Fatal("疲劳失败后未准确保留未完成部分")
		}
	})
	f.flush(t)
	battlePassNotifyPacket(t, f.conn, 1)
	if f.account.reads.Load() != a0+2 || f.activity.reads.Load() != f0+4 {
		t.Fatal("账号与疲劳的补读次数异常")
	}
	playerStatsEdit(t, f.s, f.x, func() {
		p := f.x.battlePassPush
		if p.accountDirty || p.fatigueDirty || p.retryAttempt != 0 {
			t.Fatal("成功后补试状态未清理")
		}
	})
}

func TestBattlePassPushRetryIsBoundedAndNewEventRearms(t *testing.T) {
	q := battlePassRetryClock()
	f := newBattlePassRetryFixture(t, q)
	f.claim(t)
	f.account.unavailable.Store(true)
	f.g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true})
	for _, delay := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		f.flush(t)
		q.mu.Lock()
		task := q.active[f.x.battlePassPush.key]
		valid := task != nil && task.delay == delay
		q.mu.Unlock()
		if !valid {
			t.Fatal("失败未按退避间隔安排补试", delay)
		}
	}
	f.flush(t)
	q.mu.Lock()
	pending := q.active[f.x.battlePassPush.key] != nil
	q.mu.Unlock()
	if pending {
		t.Fatal("五次补试之后仍有无限重试定时器")
	}
	f.account.unavailable.Store(false)
	f.g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true})
	f.flush(t)
	battlePassNotifyPacket(t, f.conn, 1)
}

func TestBattlePassPushRetryStopsWithSubscriptionOrActivity(t *testing.T) {
	for _, mode := range []string{"取消订阅", "撤销登录", "活动结束", "活动关闭"} {
		t.Run(mode, func(t *testing.T) {
			q := battlePassRetryClock()
			f := newBattlePassRetryFixture(t, q)
			f.account.failNext.Store(true)
			f.g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true})
			f.flush(t)
			p := f.x.battlePassPush
			q.mu.Lock()
			stale := q.active[p.key]
			q.mu.Unlock()
			if stale == nil {
				t.Fatal("夹具没有待补试定时器")
			}
			before := f.account.reads.Load()
			playerStatsEdit(t, f.s, f.x, func() {
				switch mode {
				case "取消订阅":
					p.stop(f.s)
				case "撤销登录":
					f.x.loginRevoked.Store(true)
					// 在同一会话事件中验证，再恢复夹具入口以便执行清理。
					err := f.s.flushBattlePassPush(f.x, p)
					f.x.loginRevoked.Store(false)
					if err != nil {
						t.Fatal(err)
					}
				case "活动结束":
					policy, _ := currentBattlePassPolicy()
					q.mu.Lock()
					q.now = policy.End
					q.mu.Unlock()
				case "活动关闭":
					var err error
					f.s.activityControls, err = onlineevent.NewControls(context.Background(), f.g.Settings, []onlineevent.ControlDefinition{{ID: 2520, Name: "战令", DefaultEnabled: false}})
					if err != nil {
						t.Fatal(err)
					}
				}
			})
			f.flush(t)
			if mode == "活动结束" || mode == "活动关闭" {
				battlePassNotifyPacket(t, f.conn, 0)
			}
			q.fire(stale, true)
			if f.account.reads.Load() != before || f.g.BattlePassChanges.Interested("a") {
				t.Fatal("停止后仍读取账号或保留订阅")
			}
			q.mu.Lock()
			left := len(q.active)
			q.mu.Unlock()
			if left != 0 {
				t.Fatal("停止后仍有定时器", left)
			}
		})
	}
}
