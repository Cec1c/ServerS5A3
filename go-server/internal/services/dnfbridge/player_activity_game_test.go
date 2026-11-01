package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

type playerStatsObservedActivity struct {
	repo.ActivityRepository
	reads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (a *playerStatsObservedActivity) Summary(ctx context.Context, q repo.ActivityQuery) (repo.ActivitySummary, error) {
	a.reads.Add(1)
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return repo.ActivitySummary{}, ctx.Err()
		}
	}
	return a.ActivityRepository.Summary(ctx, q)
}

type playerStatsWireConn struct {
	bufferConn
	packets chan []byte
}

func (c *playerStatsWireConn) Write(b []byte) (int, error) {
	c.packets <- append([]byte(nil), b...)
	return len(b), nil
}

func playerStatsGameFixture(t *testing.T) (*Service, *gameSession, *playerStatsObservedActivity, *playerStatsWireConn) {
	t.Helper()
	s, _, _ := playerStatsFixture(t)
	g, _ := s.repositoryProvider()
	a := &playerStatsObservedActivity{ActivityRepository: g.Activity}
	g.Activity = a
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	ctx, cancel := context.WithCancel(context.Background())
	c := &playerStatsWireConn{packets: make(chan []byte, 32)}
	sess := &gameSession{accountID: "a", accountFromToken: true, selectedCharacterID: 2, characterGeneration: 1, ctx: ctx, conn: c, connID: "统计隔离"}
	s.gameSessions = map[uint16]*gameSession{2: sess}
	if err := s.startGameSessionEvents(ctx, sess); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.stopGameSessionEvents(stopCtx, sess)
	})
	return s, sess, a, c
}
func playerStatsRequestBytes(id uint32) []byte {
	b := []byte("PSQ1\x00\x00\x00\x00\x09\x00\x00\x00")
	binary.LittleEndian.PutUint32(b[4:8], id)
	return b
}
func playerStatsCall(t *testing.T, s *Service, session *gameSession, id uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.callGameSession(ctx, session, "统计测试请求", func() error {
		return s.handleGameCommand(session, 1, playerStatsGameOpcode, playerStatsRequestBytes(id))
	}); err != nil {
		t.Fatal(err)
	}
}
func playerStatsPacket(t *testing.T, c *playerStatsWireConn) []byte {
	t.Helper()
	select {
	case b := <-c.packets:
		if len(b) < 36 || b[0] != 1 || binary.LittleEndian.Uint16(b[1:3]) != playerStatsGameOpcode || int(binary.LittleEndian.Uint32(b[3:7])) != len(b) || b[15] != 0 || string(b[16:20]) != "PSR1" {
			t.Fatalf("游戏回包格式错误：%x", b)
		}
		return b
	case <-time.After(4 * time.Second):
		t.Fatal("未收到统计回包")
	}
	return nil
}
func playerStatsEdit(t *testing.T, s *Service, x *gameSession, fn func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.callGameSession(ctx, x, "统计测试编辑", func() error { fn(); return nil }); err != nil {
		t.Fatal(err)
	}
}
func playerStatsWaitIdle(t *testing.T, x *gameSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for x.playerStats.busy.Load() {
		if time.Now().After(deadline) {
			t.Fatal("统计工作没有退出")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPlayerStatsGameTransportCacheAndNonBlocking(t *testing.T) {
	s, x, a, c := playerStatsGameFixture(t)
	a.entered = make(chan struct{}, 1)
	a.release = make(chan struct{})
	playerStatsCall(t, s, x, 7)
	<-a.entered
	// 仓储仍阻塞时，真实会话循环已经能继续处理其它事件。
	playerStatsEdit(t, s, x, func() {})
	for i := 0; i < 20; i++ {
		playerStatsCall(t, s, x, uint32(8+i))
	}
	if a.reads.Load() != 1 {
		t.Fatal("重复点击排了额外查询", a.reads.Load())
	}
	close(a.release)
	first := playerStatsPacket(t, c)
	playerStatsWaitIdle(t, x)
	if first[28] != 0 || binary.LittleEndian.Uint32(first[20:24]) != 7 || string(first[36:40]) != "PSA2" || a.reads.Load() != 3 {
		t.Fatalf("统计结果/查询次数异常：%d", a.reads.Load())
	}
	playerStatsEdit(t, s, x, func() { x.playerStats.last = time.Time{} })
	playerStatsCall(t, s, x, 30)
	second := playerStatsPacket(t, c)
	if !bytes.Equal(first[36:], second[36:]) || a.reads.Load() != 3 || binary.LittleEndian.Uint32(second[20:24]) != 30 {
		t.Fatal("短期缓存失效或没有对应请求号")
	}
	playerStatsEdit(t, s, x, func() { x.playerStats.last = time.Time{}; x.playerStats.cacheDay = "过期日" })
	playerStatsCall(t, s, x, 31)
	_ = playerStatsPacket(t, c)
	if a.reads.Load() != 6 {
		t.Fatal("跨日没有刷新", a.reads.Load())
	}
}

func TestPlayerStatsGameTransportRejectsIdentityAndStale(t *testing.T) {
	for _, name := range []string{"未令牌认证", "空账号", "替换登录", "角色无绑定", "换角迟到", "退出迟到", "换账号迟到"} {
		t.Run(name, func(t *testing.T) {
			s, x, a, c := playerStatsGameFixture(t)
			late := strings.Contains(name, "迟到")
			if late {
				a.entered = make(chan struct{}, 1)
				a.release = make(chan struct{})
			} else {
				playerStatsEdit(t, s, x, func() {
					switch name {
					case "未令牌认证":
						x.accountFromToken = false
					case "空账号":
						x.accountID = ""
					case "替换登录":
						x.loginRevoked.Store(true)
					case "角色无绑定":
						s.mu.Lock()
						delete(s.gameSessions, 2)
						s.mu.Unlock()
					}
				})
			}
			if name == "替换登录" {
				if _, _, ok := s.playerStatsGameIdentity(x); ok {
					t.Fatal("替换登录可读")
				}
				return
			}
			playerStatsCall(t, s, x, 1)
			if late {
				<-a.entered
				playerStatsEdit(t, s, x, func() {
					switch name {
					case "换角迟到":
						s.mu.Lock()
						x.characterGeneration++
						s.mu.Unlock()
					case "退出迟到":
						x.events.active.Store(false)
					case "换账号迟到":
						x.accountID = "b"
					}
				})
				close(a.release)
				playerStatsWaitIdle(t, x)
				playerStatsEdit(t, s, x, func() {})
			}
			select {
			case b := <-c.packets:
				t.Fatalf("失效会话收到统计：%x", b)
			default:
			}
			if !late && a.reads.Load() != 0 {
				t.Fatal("无权身份访问账本")
			}
		})
	}
}

func TestPlayerStatsGameTransportAccountIsolationAndCapacity(t *testing.T) {
	s, x, a, c := playerStatsGameFixture(t)
	playerStatsCall(t, s, x, 1)
	one := playerStatsPacket(t, c)
	playerStatsWaitIdle(t, x)
	playerStatsEdit(t, s, x, func() {
		s.mu.Lock()
		delete(s.gameSessions, 2)
		x.selectedCharacterID = 99
		x.characterGeneration++
		x.accountID = "b"
		s.gameSessions[99] = x
		s.mu.Unlock()
		x.playerStats.last = time.Time{}
	})
	playerStatsCall(t, s, x, 2)
	two := playerStatsPacket(t, c)
	if bytes.Equal(one[36:], two[36:]) || a.reads.Load() != 6 || !bytes.Contains(two[36:], []byte("其它账号")) || bytes.Contains(two[36:], []byte("当前角色")) {
		t.Fatal("两个账号数据未隔离")
	}
	for n := 0; n < playerStatsGameMaxWorkers; n++ {
		if !s.playerStatsQueries.acquire() {
			t.Fatal("容量提前耗尽")
		}
	}
	if s.playerStatsQueries.acquire() {
		t.Fatal("超过全局并发上限")
	}
	for n := 0; n < playerStatsGameMaxWorkers; n++ {
		s.playerStatsQueries.release()
	}
	for _, body := range [][]byte{nil, []byte("PSQ1"), append(playerStatsRequestBytes(1), 0), playerStatsRequestBytes(0), []byte("PSQ100000000000000account=b")} {
		if _, ok := decodeGamePlayerStatsRequest(body); ok {
			t.Fatal("接受畸形查询")
		}
	}
	b := encodeGamePlayerStatsReply(gamePlayerStatsRequest{1, 9}, 0, make([]byte, playerStatsGameMaxData+1))
	if len(b) != 20 || b[12] != 5 {
		t.Fatal("超限回复未关闭")
	}
}

func TestPlayerStatsGameTransportFullQueueDropsResult(t *testing.T) {
	s, x, a, c := playerStatsGameFixture(t)
	a.entered = make(chan struct{}, 1)
	a.release = make(chan struct{})
	playerStatsCall(t, s, x, 1)
	<-a.entered
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := x.events.loop.Submit(eventloop.Event{Type: "测试占用", Priority: eventloop.PriorityHigh, Data: gameSessionEventTask{source: "测试占用", run: func() error { close(entered); <-release; return nil }}}); err != nil {
		t.Fatal(err)
	}
	<-entered
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := x.events.loop.Submit(eventloop.Event{Type: "测试满队列", Priority: eventloop.PriorityLow, Data: gameSessionEventTask{source: "测试满队列", run: func() error { return nil }}}); err != nil {
			t.Fatal(i, err)
		}
	}
	close(a.release)
	playerStatsWaitIdle(t, x)
	select {
	case <-c.packets:
		t.Fatal("队列满仍发包")
	default:
	}
}

func TestPlayerStatsGameTransportSharedFixture(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	q := read("player_stats_game_request.hex")
	r, ok := decodeGamePlayerStatsRequest(q)
	if !ok || r.id != 0x12345678 || r.epoch != 9 {
		t.Fatal(r)
	}
	want := read("player_stats_game_reply.hex")
	data := read("player_activity_psa2.hex")
	got := encodeGamePlayerStatsReply(r, 0, data)
	if !bytes.Equal(got, want) {
		t.Fatal("C++/Go共用回包夹具不符")
	}
}
