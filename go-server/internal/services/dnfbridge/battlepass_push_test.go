package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func battlePassNotifyPacket(t *testing.T, c *playerStatsWireConn, want byte) uint32 {
	t.Helper()
	select {
	case p := <-c.packets:
		if len(p) != 44 || string(p[16:20]) != "BPN2" || p[28] != 0 || binary.LittleEndian.Uint32(p[32:]) != 8 || p[36] != want {
			t.Fatalf("主动提醒包不符 %x，期望%02x", p, want)
		}
		v := binary.LittleEndian.Uint32(p[40:])
		if v == 0 {
			t.Fatal("缺少通知版本")
		}
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("未收到主动提醒")
	}
	return 0
}
func TestBattlePassPushCompletionWithoutOpening(t *testing.T) {
	s, x, activity, conn := playerStatsGameFixture(t)
	g, _ := s.repositoryProvider()
	g.BattlePassChanges = repo.NewBattlePassChangeFeed()
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.FixedZone("CN", 28800))
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	c := &bp.Catalog{Missions: map[uint32]bp.Mission{}, Daily: [][4]uint32{{1, 2, 3, 4}}, Weekly: [][4]uint32{{5, 6, 7, 8}}}
	for i := uint32(1); i <= 26; i++ {
		c.Missions[i] = bp.Mission{ID: i, Kind: 0, Param: -1, Target: 1000, XP: 100}
		if i >= 9 {
			c.Season = append(c.Season, i)
		}
	}
	m := c.Missions[1]
	m.Kind = 1
	m.Target = 2
	c.Missions[1] = m
	for _, id := range []uint32{5, 9} {
		m := c.Missions[id]
		m.Kind = 17
		m.Target = 1
		c.Missions[id] = m
	}
	for i := 1; i < 80; i++ {
		c.Experience[i] = 1300
	}
	s.battlePassCatalog = c
	s.battlePassFingerprint = "notify-fixture"
	if e := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"keep": "yes"}}); e != nil {
		t.Fatal(e)
	}
	request := []byte{'B', 'P', 'E', '2', 1, 0, 0, 0, 9, 0, 0, 0}
	battlePassPreviewCall(t, s, x, 1, request)
	revision := battlePassNotifyPacket(t, conn, 1)
	reads := activity.reads.Load()
	flush := func() {
		playerStatsEdit(t, s, x, func() {
			if e := s.flushBattlePassPush(x, x.battlePassPush); e != nil {
				t.Fatal(e)
			}
		})
	}
	publish := func(kind int, value int64) {
		g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", CharacterID: "2", Counters: map[string]int64{fmt.Sprintf("battlepass2:2026-10-31:%d:-1", kind): value}})
		flush()
	}
	publish(1, 1)
	if len(conn.packets) != 0 {
		t.Fatal("未达标发了重复提醒")
	}
	publish(1, 2)
	if v := battlePassNotifyPacket(t, conn, 3); v <= revision {
		t.Fatal("版本未递增")
	} else {
		revision = v
	}
	publish(1, 1)
	if len(conn.packets) != 0 {
		t.Fatal("旧计数倒退或重复发包")
	}
	publish(17, 1)
	if v := battlePassNotifyPacket(t, conn, 15); v <= revision {
		t.Fatal("版本未递增")
	}
	if activity.reads.Load() != reads {
		t.Fatal("击杀/徽章提醒额外查询行为库")
	}
	// 领取后同一通知路径清除三类任务位；不打开窗口、不发送BPQ。
	p, _ := currentBattlePassPolicy()
	state := bp.State{Season: p.ID, Level: 1, Day: bp.DayNumber(now), Week: bp.WeekNumber(now), DailyClaimed: 1, WeeklyClaimed: 1, SeasonClaimed: 1, StartUnix: p.Start.Unix(), EndUnix: p.End.Unix(), ConfigFingerprint: "notify-fixture"}
	data, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	a, _, _ := g.Account.Load(context.Background(), "a")
	a.Metadata[bp.MetadataKey] = string(data)
	if e := g.Account.Save(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", Account: true})
	flush()
	battlePassNotifyPacket(t, conn, 1)
	g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "b", CharacterID: "2", Counters: map[string]int64{"battlepass2:2026-10-31:1:-1": 999}})
	flush()
	if len(conn.packets) != 0 {
		t.Fatal("串账号提醒")
	}
	// 跨06点后每日清除领取位，新一天没有计数；周/赛季已领保持。
	now = now.Add(24 * time.Hour)
	flush()
	if len(conn.packets) != 0 {
		t.Fatal("跨日旧计数仍提醒")
	}
	playerStatsEdit(t, s, x, func() { x.battlePassPush.stop(s) })
	if g.BattlePassChanges.Interested("a") {
		t.Fatal("离开角色后仍保留订阅")
	}
}

// 使用真实进程调度队列、会话循环和订阅回调，不手工调用flush。
func TestBattlePassPushSchedulerNoIdlePolling(t *testing.T) {
	s, x, activity, conn := playerStatsGameFixture(t)
	g, _ := s.repositoryProvider()
	g.BattlePassChanges = repo.NewBattlePassChangeFeed()
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	c := &bp.Catalog{Missions: map[uint32]bp.Mission{}, Daily: [][4]uint32{{1, 2, 3, 4}}, Weekly: [][4]uint32{{5, 6, 7, 8}}}
	for i := uint32(1); i <= 26; i++ {
		c.Missions[i] = bp.Mission{ID: i, Kind: 0, Param: -1, Target: 1000, XP: 100}
		if i >= 9 {
			c.Season = append(c.Season, i)
		}
	}
	m := c.Missions[1]
	m.Kind = 1
	m.Target = 2
	c.Missions[1] = m
	for i := 1; i < 80; i++ {
		c.Experience[i] = 1300
	}
	s.battlePassCatalog = c
	s.battlePassFingerprint = "notify-fixture"
	if e := g.Account.Save(context.Background(), repo.AccountRecord{AccountID: "a"}); e != nil {
		t.Fatal(e)
	}
	clock := newProcessGameplayTimeQueue(nil)
	s.gameplayTimers = clock
	timerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { clock.Start(timerCtx); close(done) }()
	defer func() { cancel(); <-done }()
	battlePassPreviewCall(t, s, x, 1, []byte{'B', 'P', 'E', '2', 3, 0, 0, 0, 9, 0, 0, 0})
	battlePassNotifyPacket(t, conn, 1)
	reads := activity.reads.Load()
	key := fmt.Sprintf("battlepass2:%s:1:-1", repo.ActivityDay(s.gameplayNow()))
	// 提交回调运行时占有全局服务锁，提醒队列也必须能立即返回。
	s.mu.Lock()
	g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", CharacterID: "2", Counters: map[string]int64{key: 2}})
	s.mu.Unlock()
	battlePassNotifyPacket(t, conn, 3)
	g.BattlePassChanges.Publish(repo.BattlePassChange{AccountID: "a", CharacterID: "2", Counters: map[string]int64{key: 2}})
	select {
	case p := <-conn.packets:
		t.Fatalf("无状态变化仍发包%x", p)
	case <-time.After(250 * time.Millisecond):
	}
	if activity.reads.Load() != reads {
		t.Fatal("计数推送重复查行为库")
	}
	playerStatsEdit(t, s, x, func() { x.battlePassPush.stop(s) })
	if g.BattlePassChanges.Interested("a") {
		t.Fatal("停止后仍保留订阅")
	}
}
